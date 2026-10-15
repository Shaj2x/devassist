//go:build integration

package kafkax

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

func brokers() []string {
	if b := os.Getenv("KAFKA_BOOTSTRAP_SERVERS"); b != "" {
		return []string{b}
	}
	return []string{"localhost:29092"}
}

func createTopic(t *testing.T, topic string) {
	t.Helper()
	conn, err := kafka.Dial("tcp", brokers()[0])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	controller, err := conn.Controller()
	if err != nil {
		t.Fatal(err)
	}
	cc, err := kafka.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	if err := cc.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: 3, ReplicationFactor: 1}); err != nil {
		t.Fatal(err)
	}
}

// A reader that starts before its topic exists must start consuming once the
// topic is created. In Kubernetes every pod starts at once, so a consumer can
// easily join its group before the topic-creation Job has reached its topic.
func TestReaderPicksUpTopicCreatedAfterItStarted(t *testing.T) {
	topic := fmt.Sprintf("devassist-test-late-topic-%d", time.Now().UnixNano())
	reader := NewReader(brokers(), topic+"-group", topic)
	defer reader.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	got := make(chan kafka.Message, 1)
	go func() {
		if msg, err := reader.FetchMessage(ctx); err == nil {
			got <- msg
		}
	}()

	time.Sleep(5 * time.Second) // let the reader join its group with no partitions
	createTopic(t, topic)
	w := &kafka.Writer{Addr: kafka.TCP(brokers()...), Topic: topic, AllowAutoTopicCreation: false}
	defer w.Close()
	if err := w.WriteMessages(ctx, kafka.Message{Key: []byte("k"), Value: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-got:
		if string(msg.Value) != "hello" {
			t.Fatalf("got %q", msg.Value)
		}
	case <-ctx.Done():
		t.Fatal("reader never received a message from a topic created after it started")
	}
}
