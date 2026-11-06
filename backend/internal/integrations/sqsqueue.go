package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
)

// sqsMaxWait is the longest long-poll SQS allows.
const sqsMaxWait = 20 * time.Second

// sqsAPI is the subset of the SQS client the adapter uses, so tests can substitute a fake.
type sqsAPI interface {
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// SQSQueue is the production Queue, backed by an AWS SQS standard queue.
//
//	Publish:  SendMessage (body = job ID)
//	Receive:  ReceiveMessage, long-polling, one message at a time
//	Ack:      DeleteMessage with the receipt handle of this delivery
//	Requeue:  SendMessage
//
// SQS delivers at least once and may deliver duplicates, which the worker already
// tolerates because PostgreSQL is the source of truth (a duplicate finds the job
// claimed or finished and is acknowledged without doing work).
//
// Differences from the Redis queue:
//   - Requeue cannot delete the previous in-flight copy, because only the receiver holds
//     its receipt handle. The stale copy reappears when its visibility timeout ends and
//     is then a harmless duplicate.
//   - A message that is received but never acknowledged returns to the queue after the
//     visibility timeout. After maxReceiveCount deliveries SQS moves it to the
//     dead-letter queue (configured in Terraform), where an alarm fires. The job row in
//     PostgreSQL is untouched, so the sweeper can still recover the job.
type SQSQueue struct {
	client     sqsAPI
	queueURL   string
	visibility time.Duration
}

// NewSQSQueue builds the queue using the AWS default credential chain (the ECS task role).
func NewSQSQueue(ctx context.Context, cfg config.SQSConfig) (*SQSQueue, error) {
	base, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return newSQSQueue(sqs.NewFromConfig(base), cfg), nil
}

func newSQSQueue(client sqsAPI, cfg config.SQSConfig) *SQSQueue {
	return &SQSQueue{client: client, queueURL: cfg.QueueURL, visibility: cfg.VisibilityTimeout}
}

func (q *SQSQueue) Publish(ctx context.Context, jobID string) error {
	_, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.queueURL),
		MessageBody: aws.String(jobID),
	})
	if err != nil {
		return fmt.Errorf("sqs send message: %w", err)
	}
	return nil
}

// Receive long-polls for up to wait (rounded up to whole seconds, at most 20). It
// returns (nil, nil) when nothing arrived.
func (q *SQSQueue) Receive(ctx context.Context, wait time.Duration) (*Delivery, error) {
	secs := int32((min(max(wait, 0), sqsMaxWait) + time.Second - 1) / time.Second)
	in := &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.queueURL),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     secs,
	}
	if q.visibility > 0 {
		in.VisibilityTimeout = int32(q.visibility / time.Second)
	}
	out, err := q.client.ReceiveMessage(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("sqs receive message: %w", err)
	}
	if len(out.Messages) == 0 {
		return nil, nil
	}
	m := out.Messages[0]
	if m.Body == nil || m.ReceiptHandle == nil {
		return nil, errors.New("sqs returned a message without a body or receipt handle")
	}
	return &Delivery{JobID: *m.Body, receipt: *m.ReceiptHandle}, nil
}

func (q *SQSQueue) Ack(ctx context.Context, d *Delivery) error {
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.queueURL),
		ReceiptHandle: aws.String(d.receipt),
	})
	if err != nil {
		return fmt.Errorf("sqs delete message: %w", err)
	}
	return nil
}

func (q *SQSQueue) Requeue(ctx context.Context, jobID string) error { return q.Publish(ctx, jobID) }
