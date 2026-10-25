// Package redisx — минимальный клиент Redis (RESP2) на stdlib: PING, SET с TTL, GET, DEL.
//
// Зачем свой клиент: платформе нужны три команды (координаты курьера с TTL), а go-redis тянет
// десяток транзитивных зависимостей. Клиент потокобезопасен, держит небольшой пул соединений,
// на каждую команду выставляет дедлайн и не повторяет команды сам: SET/GET/DEL здесь идемпотентны,
// повтор — решение вызывающего. Кластер, pub/sub, pipelining и TLS не поддерживаются
// (managed Redis в облаке — отдельный адаптер при необходимости).
package redisx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNil — ключа нет (GET).
var ErrNil = errors.New("redisx: key not found")

// Options — настройки клиента.
type Options struct {
	Addr        string        // host:port
	Password    string        // пусто — без AUTH
	DB          int           // номер БД (SELECT), по умолчанию 0
	DialTimeout time.Duration // по умолчанию 1 с
	CmdTimeout  time.Duration // дедлайн одной команды, по умолчанию 1 с
	PoolSize    int           // максимум простаивающих соединений, по умолчанию 4
}

// Client — клиент Redis.
type Client struct {
	o    Options
	mu   sync.Mutex
	idle []*conn
	shut bool
}

type conn struct {
	c net.Conn
	r *bufio.Reader
}

// New создаёт клиента. Соединение устанавливается лениво, при первой команде.
func New(o Options) (*Client, error) {
	if strings.TrimSpace(o.Addr) == "" {
		return nil, errors.New("redisx: Addr is required")
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = time.Second
	}
	if o.CmdTimeout <= 0 {
		o.CmdTimeout = time.Second
	}
	if o.PoolSize <= 0 {
		o.PoolSize = 4
	}
	return &Client{o: o}, nil
}

// Close закрывает простаивающие соединения; последующие команды возвращают ошибку.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shut = true
	for _, cn := range c.idle {
		_ = cn.c.Close()
	}
	c.idle = nil
}

func (c *Client) get(ctx context.Context) (*conn, error) {
	c.mu.Lock()
	if c.shut {
		c.mu.Unlock()
		return nil, errors.New("redisx: client is closed")
	}
	if n := len(c.idle); n > 0 {
		cn := c.idle[n-1]
		c.idle = c.idle[:n-1]
		c.mu.Unlock()
		return cn, nil
	}
	c.mu.Unlock()
	d := net.Dialer{Timeout: c.o.DialTimeout}
	nc, err := d.DialContext(ctx, "tcp", c.o.Addr)
	if err != nil {
		return nil, fmt.Errorf("redisx: dial %s: %w", c.o.Addr, err)
	}
	cn := &conn{c: nc, r: bufio.NewReader(nc)}
	if c.o.Password != "" {
		if _, err := c.roundTrip(ctx, cn, "AUTH", c.o.Password); err != nil {
			_ = nc.Close()
			return nil, err
		}
	}
	if c.o.DB != 0 {
		if _, err := c.roundTrip(ctx, cn, "SELECT", strconv.Itoa(c.o.DB)); err != nil {
			_ = nc.Close()
			return nil, err
		}
	}
	return cn, nil
}

func (c *Client) put(cn *conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shut || len(c.idle) >= c.o.PoolSize {
		_ = cn.c.Close()
		return
	}
	c.idle = append(c.idle, cn)
}

// reply — разобранный ответ.
type reply struct {
	kind byte // + - : $
	str  string
	num  int64
	nil_ bool
}

// ServerError — Redis вернул ошибку (-ERR …): соединение остаётся рабочим.
type ServerError string

func (e ServerError) Error() string { return "redisx: server: " + string(e) }

func (c *Client) do(ctx context.Context, args ...string) (reply, error) {
	cn, err := c.get(ctx)
	if err != nil {
		return reply{}, err
	}
	rep, err := c.roundTrip(ctx, cn, args...)
	var se ServerError
	if err != nil && !errors.As(err, &se) {
		_ = cn.c.Close() // состояние протокола неизвестно — соединение выбрасываем
		return reply{}, err
	}
	c.put(cn)
	return rep, err
}

func (c *Client) roundTrip(ctx context.Context, cn *conn, args ...string) (reply, error) {
	dl := time.Now().Add(c.o.CmdTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	if err := cn.c.SetDeadline(dl); err != nil {
		return reply{}, err
	}
	if _, err := cn.c.Write(encode(args)); err != nil {
		return reply{}, fmt.Errorf("redisx: write: %w", err)
	}
	return readReply(cn.r)
}

func encode(args []string) []byte {
	b := make([]byte, 0, 32)
	b = append(b, '*')
	b = strconv.AppendInt(b, int64(len(args)), 10)
	b = append(b, '\r', '\n')
	for _, a := range args {
		b = append(b, '$')
		b = strconv.AppendInt(b, int64(len(a)), 10)
		b = append(b, '\r', '\n')
		b = append(b, a...)
		b = append(b, '\r', '\n')
	}
	return b
}

const maxBulk = 1 << 20

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("redisx: read: %w", err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func readReply(r *bufio.Reader) (reply, error) {
	line, err := readLine(r)
	if err != nil {
		return reply{}, err
	}
	if line == "" {
		return reply{}, errors.New("redisx: empty reply")
	}
	kind, rest := line[0], line[1:]
	switch kind {
	case '+':
		return reply{kind: '+', str: rest}, nil
	case '-':
		return reply{}, ServerError(rest)
	case ':':
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return reply{}, fmt.Errorf("redisx: bad integer %q", rest)
		}
		return reply{kind: ':', num: n}, nil
	case '$':
		n, err := strconv.Atoi(rest)
		if err != nil || n < -1 || n > maxBulk {
			return reply{}, fmt.Errorf("redisx: bad bulk length %q", rest)
		}
		if n == -1 {
			return reply{kind: '$', nil_: true}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return reply{}, fmt.Errorf("redisx: read bulk: %w", err)
		}
		return reply{kind: '$', str: string(buf[:n])}, nil
	}
	return reply{}, fmt.Errorf("redisx: unsupported reply %q", line)
}

// Ping проверяет связь.
func (c *Client) Ping(ctx context.Context) error {
	rep, err := c.do(ctx, "PING")
	if err != nil {
		return err
	}
	if rep.str != "PONG" {
		return fmt.Errorf("redisx: unexpected PING reply %q", rep.str)
	}
	return nil
}

// Set записывает значение с временем жизни (ttl > 0 обязателен: ключи платформы всегда эфемерны).
func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("redisx: ttl must be positive")
	}
	_, err := c.do(ctx, "SET", key, value, "PX", strconv.FormatInt(ttl.Milliseconds(), 10))
	return err
}

// Get читает значение; ErrNil — ключа нет или он истёк.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	rep, err := c.do(ctx, "GET", key)
	if err != nil {
		return "", err
	}
	if rep.nil_ {
		return "", ErrNil
	}
	return rep.str, nil
}

// Del удаляет ключи и возвращает число удалённых.
func (c *Client) Del(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	rep, err := c.do(ctx, append([]string{"DEL"}, keys...)...)
	return rep.num, err
}
