package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"cloud.google.com/go/pubsub/v2"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

const transcriptionJobMessageType = "scribe.transcription_job"

type PubSubTranscriptionQueue struct {
	client     *pubsub.Client
	publisher  *pubsub.Publisher
	subscriber *pubsub.Subscriber
}

type transcriptionJobMessage struct {
	Type  string `json:"type"`
	JobID uint64 `json:"job_id"`
}

func NewPubSubTranscriptionQueue(ctx context.Context, cfg config.TranscriptionQueue, workerCount int) (*PubSubTranscriptionQueue, error) {
	projectID := strings.TrimSpace(cfg.ProjectID)
	topicID := strings.TrimSpace(cfg.TopicID)
	subscriptionID := strings.TrimSpace(cfg.SubscriptionID)
	if projectID == "" || topicID == "" || subscriptionID == "" {
		return nil, fmt.Errorf("pubsub transcription queue requires project_id, topic_id, and subscription_id")
	}
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("create pubsub client: %w", err)
	}
	q := &PubSubTranscriptionQueue{
		client:     client,
		publisher:  client.Publisher(topicID),
		subscriber: client.Subscriber(subscriptionID),
	}
	maxOutstanding := cfg.MaxOutstandingMessages
	if maxOutstanding <= 0 {
		maxOutstanding = workerCount
	}
	if maxOutstanding <= 0 {
		maxOutstanding = 1
	}
	q.subscriber.ReceiveSettings.MaxOutstandingMessages = maxOutstanding
	q.subscriber.ReceiveSettings.NumGoroutines = workerCount
	q.subscriber.ReceiveSettings.EnablePerStreamFlowControl = true
	if cfg.MaxExtension > 0 {
		q.subscriber.ReceiveSettings.MaxExtension = cfg.MaxExtension
	}
	return q, nil
}

func Enabled(cfg config.TranscriptionQueue) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.Backend), "pubsub")
}

func (q *PubSubTranscriptionQueue) PublishTranscriptionJob(ctx context.Context, jobID uint64) error {
	if q == nil || q.publisher == nil || jobID == 0 {
		return nil
	}
	body, err := json.Marshal(transcriptionJobMessage{Type: transcriptionJobMessageType, JobID: jobID})
	if err != nil {
		return err
	}
	result := q.publisher.Publish(ctx, &pubsub.Message{
		Data: body,
		Attributes: map[string]string{
			"type":   transcriptionJobMessageType,
			"job_id": strconv.FormatUint(jobID, 10),
		},
	})
	if _, err := result.Get(ctx); err != nil {
		return fmt.Errorf("publish transcription job %d: %w", jobID, err)
	}
	return nil
}

func (q *PubSubTranscriptionQueue) ReceiveTranscriptionJobs(
	ctx context.Context,
	handle func(context.Context, uint64) error,
	poison func(context.Context, string, error, []byte),
) error {
	if q == nil || q.subscriber == nil {
		return nil
	}
	err := q.subscriber.Receive(ctx, func(msgCtx context.Context, msg *pubsub.Message) {
		jobID, err := parseTranscriptionJobMessage(msg)
		if err != nil {
			slog.Warn("Dropping invalid transcription job message", "message_id", msg.ID, "error", err)
			if poison != nil {
				poison(msgCtx, msg.ID, err, msg.Data)
			}
			msg.Ack()
			return
		}
		if err := handle(msgCtx, jobID); err != nil {
			slog.Warn("Transcription job message failed", "message_id", msg.ID, "job_id", jobID, "error", err)
			msg.Nack()
			return
		}
		msg.Ack()
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (q *PubSubTranscriptionQueue) Close() error {
	if q == nil {
		return nil
	}
	if q.publisher != nil {
		q.publisher.Stop()
	}
	if q.client != nil {
		return q.client.Close()
	}
	return nil
}

func parseTranscriptionJobMessage(msg *pubsub.Message) (uint64, error) {
	if msg == nil {
		return 0, fmt.Errorf("nil pubsub message")
	}
	if raw := strings.TrimSpace(msg.Attributes["job_id"]); raw != "" {
		jobID, err := strconv.ParseUint(raw, 10, 64)
		if err == nil && jobID > 0 {
			return jobID, nil
		}
	}
	var payload transcriptionJobMessage
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		return 0, fmt.Errorf("decode message: %w", err)
	}
	if payload.Type != "" && payload.Type != transcriptionJobMessageType {
		return 0, fmt.Errorf("unexpected message type %q", payload.Type)
	}
	if payload.JobID == 0 {
		return 0, fmt.Errorf("missing job_id")
	}
	return payload.JobID, nil
}
