// kfake-broker — одиночный in-memory Kafka-брокер (franz-go kfake) для проверок без Docker (CI, песочница).
// Не для разработки на стенде: данные не сохраняются, компактация топиков не выполняется.
//
//	go run ./tools/kfake-broker -port 19092
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/twmb/franz-go/pkg/kfake"
)

const defaultTopics = "orders.state,orders.status,orders.dish.status,orders.payments.status,orders.payments.link,orders.delivery.status,production.lead.time,promotions.discount"

func main() {
	port := flag.Int("port", 19092, "порт брокера (localhost)")
	topics := flag.String("topics", defaultTopics, "топики через запятую (создаются заранее, как это делает kafka-init на стенде)")
	flag.Parse()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.Ports(*port), kfake.SeedTopics(1, strings.Split(*topics, ",")...))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	fmt.Println("kfake broker:", c.ListenAddrs())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
