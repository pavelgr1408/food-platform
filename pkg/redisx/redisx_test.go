package redisx

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer — минимальный RESP-сервер: PING/SET PX/GET/DEL/AUTH/SELECT, TTL по часам теста.
type fakeServer struct {
	ln    net.Listener
	mu    sync.Mutex
	nowMu sync.Mutex
	data  map[string]fakeVal
	now   func() time.Time
	auth  string
	cmds  []string
}

type fakeVal struct {
	v   string
	exp time.Time
}

func newFake(t *testing.T, auth ...string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, data: map[string]fakeVal{}, now: time.Now}
	if len(auth) > 0 {
		s.auth = auth[0]
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func readCmd(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sz, _ := strconv.Atoi(strings.TrimSpace(l[1:]))
		buf := make([]byte, sz+2)
		if _, err := r.Read(buf[:0]); err != nil {
			return nil, err
		}
		got := 0
		for got < sz+2 {
			k, err := r.Read(buf[got:])
			if err != nil {
				return nil, err
			}
			got += k
		}
		out = append(out, string(buf[:sz]))
	}
	return out, nil
}

func (s *fakeServer) setNow(f func() time.Time) { s.nowMu.Lock(); s.now = f; s.nowMu.Unlock() }

func (s *fakeServer) clock() time.Time { s.nowMu.Lock(); f := s.now; s.nowMu.Unlock(); return f() }

func (s *fakeServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	authed := s.auth == ""
	for {
		cmd, err := readCmd(r)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.cmds = append(s.cmds, strings.ToUpper(cmd[0]))
		s.mu.Unlock()
		switch strings.ToUpper(cmd[0]) {
		case "AUTH":
			if cmd[1] == s.auth {
				authed = true
				_, _ = c.Write([]byte("+OK\r\n"))
			} else {
				_, _ = c.Write([]byte("-WRONGPASS invalid\r\n"))
			}
		case "SELECT":
			_, _ = c.Write([]byte("+OK\r\n"))
		case "PING":
			if !authed {
				_, _ = c.Write([]byte("-NOAUTH Authentication required.\r\n"))
				continue
			}
			_, _ = c.Write([]byte("+PONG\r\n"))
		case "SET":
			ms, _ := strconv.Atoi(cmd[4])
			s.mu.Lock()
			s.data[cmd[1]] = fakeVal{v: cmd[2], exp: s.clock().Add(time.Duration(ms) * time.Millisecond)}
			s.mu.Unlock()
			_, _ = c.Write([]byte("+OK\r\n"))
		case "GET":
			s.mu.Lock()
			v, ok := s.data[cmd[1]]
			if ok && !s.clock().Before(v.exp) {
				delete(s.data, cmd[1])
				ok = false
			}
			s.mu.Unlock()
			if !ok {
				_, _ = c.Write([]byte("$-1\r\n"))
			} else {
				_, _ = c.Write([]byte("$" + strconv.Itoa(len(v.v)) + "\r\n" + v.v + "\r\n"))
			}
		case "DEL":
			n := 0
			s.mu.Lock()
			for _, k := range cmd[1:] {
				if _, ok := s.data[k]; ok {
					delete(s.data, k)
					n++
				}
			}
			s.mu.Unlock()
			_, _ = c.Write([]byte(":" + strconv.Itoa(n) + "\r\n"))
		default:
			_, _ = c.Write([]byte("-ERR unknown command\r\n"))
		}
	}
}

func TestSetGetDelTTL(t *testing.T) {
	srv := newFake(t)
	var cmu sync.Mutex
	clock := time.Now()
	srv.setNow(func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock })
	c, err := New(Options{Addr: srv.ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "k"); !errors.Is(err, ErrNil) {
		t.Fatalf("missing key: %v", err)
	}
	if err := c.Set(ctx, "k", "значение\r\nс переводом строки", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Get(ctx, "k"); err != nil || v != "значение\r\nс переводом строки" {
		t.Fatalf("binary-safe value: %q %v", v, err)
	}
	cmu.Lock()
	clock = clock.Add(6 * time.Second)
	cmu.Unlock()
	if _, err := c.Get(ctx, "k"); !errors.Is(err, ErrNil) {
		t.Fatalf("expired key must be gone: %v", err)
	}
	_ = c.Set(ctx, "a", "1", time.Minute)
	_ = c.Set(ctx, "b", "2", time.Minute)
	if n, err := c.Del(ctx, "a", "b", "zzz"); err != nil || n != 2 {
		t.Fatalf("del: %d %v", n, err)
	}
	if err := c.Set(ctx, "x", "y", 0); err == nil {
		t.Fatal("ttl must be required")
	}
}

func TestAuthAndServerError(t *testing.T) {
	srv := newFake(t, "secret")
	bad, _ := New(Options{Addr: srv.ln.Addr().String(), Password: "wrong"})
	if err := bad.Ping(context.Background()); err == nil {
		t.Fatal("wrong password must fail")
	}
	ok, _ := New(Options{Addr: srv.ln.Addr().String(), Password: "secret", DB: 2})
	defer ok.Close()
	if err := ok.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	// серверная ошибка не ломает соединение: следующая команда проходит по тому же соединению
	if _, err := ok.do(context.Background(), "BOOM"); err == nil {
		t.Fatal("unknown command must return ServerError")
	}
	if err := ok.Ping(context.Background()); err != nil {
		t.Fatalf("connection must survive a server error: %v", err)
	}
}

func TestUnavailableAndTimeout(t *testing.T) {
	c, _ := New(Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("dial to a closed port must fail")
	}
	// сервер молчит → таймаут команды
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			cn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = cn // читаем и не отвечаем
		}
	}()
	c2, _ := New(Options{Addr: ln.Addr().String(), CmdTimeout: 150 * time.Millisecond})
	start := time.Now()
	if err := c2.Ping(context.Background()); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("silent server: err=%v after %v", err, time.Since(start))
	}
	c2.Close()
	if err := c2.Ping(context.Background()); err == nil {
		t.Fatal("closed client must fail")
	}
}

func TestParallelUse(t *testing.T) {
	srv := newFake(t)
	c, _ := New(Options{Addr: srv.ln.Addr().String()})
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := "k" + strconv.Itoa(i)
			if err := c.Set(context.Background(), k, k, time.Minute); err != nil {
				t.Error(err)
				return
			}
			if v, err := c.Get(context.Background(), k); err != nil || v != k {
				t.Errorf("%s: %q %v", k, v, err)
			}
		}(i)
	}
	wg.Wait()
}

// Против настоящего Redis (TEST_REDIS_ADDR=localhost:6379): тот же набор команд.
func TestRealRedis(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set")
	}
	c, _ := New(Options{Addr: addr})
	defer c.Close()
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	key := "redisx-test:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := c.Set(ctx, key, "v\r\n1", 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Get(ctx, key); err != nil || v != "v\r\n1" {
		t.Fatalf("%q %v", v, err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := c.Get(ctx, key); !errors.Is(err, ErrNil) {
		t.Fatalf("ttl: %v", err)
	}
}
