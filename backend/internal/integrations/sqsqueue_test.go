package integrations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

// fakeSQS records calls and replays canned answers.
type fakeSQS struct {
	sent     []string
	received []*sqs.ReceiveMessageInput
	deleted  []string
	messages []sqstypes.Message
	err      error
}

func (f *fakeSQS) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.sent = append(f.sent, aws.ToString(in.MessageBody))
	return &sqs.SendMessageOutput{}, nil
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.received = append(f.received, in)
	return &sqs.ReceiveMessageOutput{Messages: f.messages}, nil
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func testSQS(f *fakeSQS) *SQSQueue {
	return newSQSQueue(f, config.SQSConfig{QueueURL: "https://sqs.example/123/q", VisibilityTimeout: 31 * time.Minute})
}

func TestSQSPublishAndRequeueSendTheJobID(t *testing.T) {
	f := &fakeSQS{}
	q := testSQS(f)
	ctx := context.Background()
	if err := q.Publish(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	if err := q.Requeue(ctx, "job-2"); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || f.sent[0] != "job-1" || f.sent[1] != "job-2" {
		t.Fatalf("sent = %v", f.sent)
	}
}

func TestSQSReceiveMapsMessageAndSettings(t *testing.T) {
	f := &fakeSQS{messages: []sqstypes.Message{{Body: aws.String("job-1"), ReceiptHandle: aws.String("rh-1")}}}
	q := testSQS(f)
	d, err := q.Receive(context.Background(), 1500*time.Millisecond)
	if err != nil || d == nil {
		t.Fatalf("Receive = %v, %v", d, err)
	}
	if d.JobID != "job-1" || d.receipt != "rh-1" {
		t.Errorf("delivery = %+v", d)
	}
	in := f.received[0]
	if in.WaitTimeSeconds != 2 {
		t.Errorf("WaitTimeSeconds = %d, want 2 (1.5s rounded up)", in.WaitTimeSeconds)
	}
	if in.MaxNumberOfMessages != 1 || in.VisibilityTimeout != 31*60 {
		t.Errorf("input = %+v", in)
	}
}

func TestSQSReceiveClampsWait(t *testing.T) {
	f := &fakeSQS{}
	q := testSQS(f)
	if _, err := q.Receive(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Receive(context.Background(), -time.Second); err != nil {
		t.Fatal(err)
	}
	if f.received[0].WaitTimeSeconds != 20 || f.received[1].WaitTimeSeconds != 0 {
		t.Errorf("waits = %d, %d", f.received[0].WaitTimeSeconds, f.received[1].WaitTimeSeconds)
	}
}

func TestSQSReceiveEmptyReturnsNil(t *testing.T) {
	d, err := testSQS(&fakeSQS{}).Receive(context.Background(), time.Second)
	if d != nil || err != nil {
		t.Fatalf("Receive = %v, %v; want nil, nil", d, err)
	}
}

func TestSQSReceiveRejectsIncompleteMessage(t *testing.T) {
	f := &fakeSQS{messages: []sqstypes.Message{{Body: aws.String("job-1")}}}
	if _, err := testSQS(f).Receive(context.Background(), time.Second); err == nil {
		t.Fatal("expected an error for a message without a receipt handle")
	}
}

func TestSQSAckDeletesByReceiptHandle(t *testing.T) {
	f := &fakeSQS{}
	q := testSQS(f)
	if err := q.Ack(context.Background(), &Delivery{JobID: "job-1", receipt: "rh-1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "rh-1" {
		t.Fatalf("deleted = %v", f.deleted)
	}
}

func TestSQSErrorsAreWrapped(t *testing.T) {
	boom := errors.New("boom")
	q := testSQS(&fakeSQS{err: boom})
	ctx := context.Background()
	if err := q.Publish(ctx, "j"); !errors.Is(err, boom) {
		t.Errorf("Publish error = %v", err)
	}
	if _, err := q.Receive(ctx, time.Second); !errors.Is(err, boom) {
		t.Errorf("Receive error = %v", err)
	}
	if err := q.Ack(ctx, &Delivery{receipt: "r"}); !errors.Is(err, boom) {
		t.Errorf("Ack error = %v", err)
	}
}
