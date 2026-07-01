package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type confirmationDoc struct {
	ID             string                    `bson:"_id"`
	UserID         int64                     `bson:"user_id"`
	IntentJSON     string                    `bson:"intent_json"`
	Status         orders.ConfirmationStatus `bson:"status"`
	CorrelationID  string                    `bson:"correlation_id"`
	IntentHash     string                    `bson:"intent_hash"`
	IdempotencyKey string                    `bson:"idempotency_key"`
	CreatedAt      time.Time                 `bson:"created_at"`
	ExpiresAt      time.Time                 `bson:"expires_at"`
	UpdatedAt      time.Time                 `bson:"updated_at"`
	Result         *executionResultDoc       `bson:"result,omitempty"`
	ErrorMessage   string                    `bson:"error_message,omitempty"`
}

type executionResultDoc struct {
	Mode          string `bson:"mode"`
	ClientOrderID string `bson:"client_order_id"`
	Message       string `bson:"message"`
	Quantity      string `bson:"quantity,omitempty"`
	Symbol        string `bson:"symbol,omitempty"`
	Side          string `bson:"side,omitempty"`
	ExitPrice     string `bson:"exit_price,omitempty"`
	RealizedPnL   string `bson:"realized_pnl,omitempty"`
}

const terminalConfirmationRetention = 90 * 24 * time.Hour

func terminalConfirmationExpiresAt(now time.Time) time.Time {
	return now.Add(terminalConfirmationRetention)
}

func (s *Store) Put(ctx context.Context, confirmation orders.Confirmation) error {
	doc, err := newConfirmationDoc(confirmation)
	if err != nil {
		return err
	}

	if _, err := s.confirmations.InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("insert confirmation: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (orders.Confirmation, bool, error) {
	var doc confirmationDoc
	err := s.confirmations.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return orders.Confirmation{}, false, nil
	}
	if err != nil {
		return orders.Confirmation{}, false, err
	}
	confirmation, err := doc.toConfirmation()
	if err != nil {
		return orders.Confirmation{}, false, err
	}
	return confirmation, true, nil
}

func (s *Store) TakeForExecution(ctx context.Context, userID int64, id string, now time.Time) (orders.Confirmation, orders.ExecutionResult, bool, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: userID},
		{Key: "status", Value: orders.StatusPending},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: orders.StatusExecuting},
		{Key: "updated_at", Value: now},
	}}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var doc confirmationDoc
	err := s.confirmations.FindOneAndUpdate(ctx, filter, update, opts).Decode(&doc)
	if err == nil {
		confirmation, err := doc.toConfirmation()
		return confirmation, orders.ExecutionResult{}, false, err
	}
	if !errors.Is(err, mongodriver.ErrNoDocuments) {
		return orders.Confirmation{}, orders.ExecutionResult{}, false, fmt.Errorf("take confirmation: %w", err)
	}

	doc, err = s.findConfirmation(ctx, id)
	if err != nil {
		return orders.Confirmation{}, orders.ExecutionResult{}, false, err
	}
	return s.resultForExistingConfirmation(ctx, doc, userID, now)
}

func (s *Store) Complete(ctx context.Context, id string, result orders.ExecutionResult) error {
	now := time.Now().UTC()
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: orders.StatusExecuted},
		{Key: "result", Value: newExecutionResultDoc(result)},
		{Key: "updated_at", Value: now},
		{Key: "expires_at", Value: terminalConfirmationExpiresAt(now)},
	}}}
	matched, err := s.updateConfirmation(ctx, id, orders.StatusExecuting, update)
	if err != nil {
		return err
	}
	if !matched {
		return orders.ErrConfirmationNotFound
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, id string, message string) error {
	now := time.Now().UTC()
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: orders.StatusFailed},
		{Key: "error_message", Value: message},
		{Key: "updated_at", Value: now},
		{Key: "expires_at", Value: terminalConfirmationExpiresAt(now)},
	}}}
	matched, err := s.updateConfirmation(ctx, id, orders.StatusExecuting, update)
	if err != nil {
		return err
	}
	if !matched {
		return orders.ErrConfirmationNotFound
	}
	return nil
}

func (s *Store) Cancel(ctx context.Context, userID int64, id string, now time.Time) error {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: userID},
		{Key: "status", Value: orders.StatusPending},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: orders.StatusCancelled},
		{Key: "updated_at", Value: now},
	}}}

	result, err := s.confirmations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("cancel confirmation: %w", err)
	}
	if result.ModifiedCount > 0 {
		return nil
	}

	doc, err := s.findConfirmation(ctx, id)
	if err != nil {
		return err
	}
	if doc.UserID != userID {
		return orders.ErrConfirmationForbidden
	}

	switch doc.Status {
	case orders.StatusCancelled:
		return nil
	case orders.StatusExecuted:
		return orders.ErrConfirmationExecuted
	case orders.StatusExecuting:
		return orders.ErrConfirmationExecuting
	case orders.StatusFailed:
		return orders.ErrConfirmationFailed
	case orders.StatusExpired:
		return orders.ErrConfirmationExpired
	case orders.StatusPending:
		if !now.Before(doc.ExpiresAt) {
			_ = s.markExpired(ctx, id, now)
			return orders.ErrConfirmationExpired
		}
		return orders.ErrConfirmationExecuting
	default:
		return fmt.Errorf("confirmation %s has unknown status %q", id, doc.Status)
	}
}

func (s *Store) updateConfirmation(ctx context.Context, id string, status orders.ConfirmationStatus, update bson.D) (bool, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: status},
	}
	result, err := s.confirmations.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, fmt.Errorf("update confirmation: %w", err)
	}
	return result.MatchedCount > 0, nil
}

func (s *Store) findConfirmation(ctx context.Context, id string) (confirmationDoc, error) {
	var doc confirmationDoc
	err := s.confirmations.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return confirmationDoc{}, orders.ErrConfirmationNotFound
	}
	if err != nil {
		return confirmationDoc{}, fmt.Errorf("find confirmation: %w", err)
	}
	return doc, nil
}

func (s *Store) resultForExistingConfirmation(ctx context.Context, doc confirmationDoc, userID int64, now time.Time) (orders.Confirmation, orders.ExecutionResult, bool, error) {
	if doc.UserID != userID {
		return orders.Confirmation{}, orders.ExecutionResult{}, false, orders.ErrConfirmationForbidden
	}

	switch doc.Status {
	case orders.StatusExecuted:
		confirmation, err := doc.toConfirmation()
		return confirmation, doc.result(), true, err
	case orders.StatusCancelled:
		return orders.Confirmation{}, orders.ExecutionResult{}, false, orders.ErrConfirmationCancelled
	case orders.StatusFailed:
		return orders.Confirmation{}, orders.ExecutionResult{}, false, orders.ErrConfirmationFailed
	case orders.StatusExecuting:
		return orders.Confirmation{}, orders.ExecutionResult{}, false, orders.ErrConfirmationExecuting
	case orders.StatusExpired:
		return orders.Confirmation{}, orders.ExecutionResult{}, false, orders.ErrConfirmationExpired
	case orders.StatusPending:
		if !now.Before(doc.ExpiresAt) {
			_ = s.markExpired(ctx, doc.ID, now)
			return orders.Confirmation{}, orders.ExecutionResult{}, false, orders.ErrConfirmationExpired
		}
		return orders.Confirmation{}, orders.ExecutionResult{}, false, orders.ErrConfirmationExecuting
	default:
		return orders.Confirmation{}, orders.ExecutionResult{}, false, fmt.Errorf("confirmation %s has unknown status %q", doc.ID, doc.Status)
	}
}

func (s *Store) markExpired(ctx context.Context, id string, now time.Time) error {
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: orders.StatusExpired},
		{Key: "updated_at", Value: now},
	}}}
	_, err := s.updateConfirmation(ctx, id, orders.StatusPending, update)
	return err
}

func newConfirmationDoc(confirmation orders.Confirmation) (confirmationDoc, error) {
	intentJSON, err := json.Marshal(confirmation.Intent)
	if err != nil {
		return confirmationDoc{}, fmt.Errorf("marshal confirmation intent: %w", err)
	}

	status := confirmation.Status
	if status == "" {
		status = orders.StatusPending
	}

	updatedAt := confirmation.CreatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	return confirmationDoc{
		ID:             confirmation.ID,
		UserID:         confirmation.UserID,
		IntentJSON:     string(intentJSON),
		Status:         status,
		CorrelationID:  confirmation.CorrelationID,
		IntentHash:     confirmation.IntentHash,
		IdempotencyKey: confirmation.IdempotencyKey,
		CreatedAt:      confirmation.CreatedAt,
		ExpiresAt:      confirmation.ExpiresAt,
		UpdatedAt:      updatedAt,
	}, nil
}

func (d confirmationDoc) toConfirmation() (orders.Confirmation, error) {
	var intent domain.Intent
	if err := json.Unmarshal([]byte(d.IntentJSON), &intent); err != nil {
		return orders.Confirmation{}, fmt.Errorf("unmarshal confirmation intent: %w", err)
	}

	return orders.Confirmation{
		ID:             d.ID,
		UserID:         d.UserID,
		Intent:         intent,
		Status:         d.Status,
		CorrelationID:  d.CorrelationID,
		IntentHash:     d.IntentHash,
		IdempotencyKey: d.IdempotencyKey,
		CreatedAt:      d.CreatedAt,
		ExpiresAt:      d.ExpiresAt,
	}, nil
}

func newExecutionResultDoc(result orders.ExecutionResult) executionResultDoc {
	return executionResultDoc{
		Mode:          result.Mode,
		ClientOrderID: result.ClientOrderID,
		Message:       result.Message,
		Quantity:      result.Quantity.String(),
		Symbol:        result.Symbol,
		Side:          result.Side,
		ExitPrice:     result.ExitPrice.String(),
		RealizedPnL:   result.RealizedPnL.String(),
	}
}

func (d confirmationDoc) result() orders.ExecutionResult {
	if d.Result == nil {
		return orders.ExecutionResult{}
	}
	return orders.ExecutionResult{
		Mode:          d.Result.Mode,
		ClientOrderID: d.Result.ClientOrderID,
		Message:       d.Result.Message,
		Quantity:      parseResultDecimal(d.Result.Quantity),
		Symbol:        d.Result.Symbol,
		Side:          d.Result.Side,
		ExitPrice:     parseResultDecimal(d.Result.ExitPrice),
		RealizedPnL:   parseResultDecimal(d.Result.RealizedPnL),
	}
}

func parseResultDecimal(s string) decimal.Decimal {
	return parseDecimal(s)
}
