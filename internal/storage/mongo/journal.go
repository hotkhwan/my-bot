package mongo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bottrade/internal/decimal"
	"bottrade/internal/journal"
)

// journalDoc stores decimals as strings: the decimal type has unexported fields
// that the bson encoder cannot reach, so persisting Decimal directly would lose
// the value.
type journalDoc struct {
	ID             string    `bson:"_id"`
	UserID         int64     `bson:"user_id"`
	CampaignID     string    `bson:"campaign_id,omitempty"`
	ConfirmationID string    `bson:"confirmation_id,omitempty"`
	Symbol         string    `bson:"symbol"`
	Side           string    `bson:"side"`
	Strategy       string    `bson:"strategy,omitempty"`
	Models         []string  `bson:"models,omitempty"`
	Reason         string    `bson:"reason,omitempty"`
	Confidence     int       `bson:"confidence,omitempty"`
	Leverage       int       `bson:"leverage"`
	Mode           string    `bson:"mode"`
	Entry          string    `bson:"entry"`
	Exit           string    `bson:"exit"`
	StopLoss       string    `bson:"stop_loss"`
	TakeProfit     string    `bson:"take_profit"`
	SizeUSDT       string    `bson:"size_usdt"`
	Quantity       string    `bson:"quantity"`
	PnLUSDT        string    `bson:"pnl_usdt"`
	Outcome        string    `bson:"outcome"`
	OpenedAt       time.Time `bson:"opened_at"`
	ClosedAt       time.Time `bson:"closed_at,omitempty"`
}

// JournalRepository persists trades in MongoDB. It implements journal.Repository.
type JournalRepository struct {
	coll *mongodriver.Collection
}

// Journal returns the MongoDB-backed trade journal repository.
func (s *Store) Journal() *JournalRepository {
	return &JournalRepository{coll: s.journalTrades}
}

// Save inserts open trades idempotently. Duplicate open writes use $setOnInsert
// so a closed row can never be overwritten back to open.
func (r *JournalRepository) Save(ctx context.Context, trade journal.Trade) error {
	doc := journalDoc{
		ID:             trade.ID,
		UserID:         trade.UserID,
		CampaignID:     trade.CampaignID,
		ConfirmationID: trade.ConfirmationID,
		Symbol:         trade.Symbol,
		Side:           trade.Side,
		Strategy:       trade.Strategy,
		Models:         trade.Models,
		Reason:         trade.Reason,
		Confidence:     trade.Confidence,
		Leverage:       trade.Leverage,
		Mode:           trade.Mode,
		Entry:          trade.Entry.String(),
		Exit:           trade.Exit.String(),
		StopLoss:       trade.StopLoss.String(),
		TakeProfit:     trade.TakeProfit.String(),
		SizeUSDT:       trade.SizeUSDT.String(),
		Quantity:       trade.Quantity.String(),
		PnLUSDT:        trade.PnLUSDT.String(),
		Outcome:        string(trade.Outcome),
		OpenedAt:       trade.OpenedAt,
		ClosedAt:       trade.ClosedAt,
	}
	if trade.Outcome == journal.OutcomeOpen {
		if _, err := r.coll.UpdateOne(ctx,
			bson.M{"_id": trade.ID},
			bson.M{"$setOnInsert": doc},
			options.UpdateOne().SetUpsert(true),
		); err != nil {
			return fmt.Errorf("save open journal trade: %w", err)
		}
		return nil
	}
	if _, err := r.coll.ReplaceOne(ctx, bson.M{"_id": trade.ID}, doc, options.Replace().SetUpsert(true)); err != nil {
		return fmt.Errorf("save journal trade: %w", err)
	}
	return nil
}

// Close atomically updates one still-open journal trade.
func (r *JournalRepository) Close(ctx context.Context, id string, update journal.CloseUpdate) (journal.Trade, bool, error) {
	set := bson.M{
		"exit":      update.Exit.String(),
		"pnl_usdt":  update.PnLUSDT.String(),
		"outcome":   string(update.Outcome),
		"closed_at": update.ClosedAt,
	}
	if strings.TrimSpace(update.Mode) != "" {
		set["mode"] = update.Mode
	}
	var doc journalDoc
	err := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id, "outcome": string(journal.OutcomeOpen)},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&doc)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return journal.Trade{}, false, nil
	}
	if err != nil {
		return journal.Trade{}, false, fmt.Errorf("close journal trade: %w", err)
	}
	return tradeFromDoc(doc), true, nil
}

// List returns the trades matching filter.
func (r *JournalRepository) List(ctx context.Context, filter journal.Filter) ([]journal.Trade, error) {
	query := bson.M{}
	if filter.UserID != 0 {
		query["user_id"] = filter.UserID
	}
	if filter.CampaignID != "" {
		query["campaign_id"] = filter.CampaignID
	}
	if filter.Symbol != "" {
		query["symbol"] = strings.ToUpper(filter.Symbol)
	}
	if filter.Strategy != "" {
		query["strategy"] = filter.Strategy
	}
	if filter.Mode != "" {
		query["mode"] = filter.Mode
	}
	if filter.ClosedOnly {
		query["outcome"] = bson.M{"$ne": string(journal.OutcomeOpen)}
	}
	if filter.OpenOnly {
		query["outcome"] = string(journal.OutcomeOpen)
	}

	cursor, err := r.coll.Find(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list journal trades: %w", err)
	}
	var docs []journalDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode journal trades: %w", err)
	}

	trades := make([]journal.Trade, 0, len(docs))
	for _, doc := range docs {
		trades = append(trades, tradeFromDoc(doc))
	}
	return trades, nil
}

func tradeFromDoc(doc journalDoc) journal.Trade {
	return journal.Trade{
		ID:             doc.ID,
		UserID:         doc.UserID,
		CampaignID:     doc.CampaignID,
		ConfirmationID: doc.ConfirmationID,
		Symbol:         doc.Symbol,
		Side:           doc.Side,
		Strategy:       doc.Strategy,
		Models:         doc.Models,
		Reason:         doc.Reason,
		Confidence:     doc.Confidence,
		Leverage:       doc.Leverage,
		Mode:           doc.Mode,
		Entry:          parseDecimal(doc.Entry),
		Exit:           parseDecimal(doc.Exit),
		StopLoss:       parseDecimal(doc.StopLoss),
		TakeProfit:     parseDecimal(doc.TakeProfit),
		SizeUSDT:       parseDecimal(doc.SizeUSDT),
		Quantity:       parseDecimal(doc.Quantity),
		PnLUSDT:        parseDecimal(doc.PnLUSDT),
		Outcome:        journal.Outcome(doc.Outcome),
		OpenedAt:       doc.OpenedAt,
		ClosedAt:       doc.ClosedAt,
	}
}

func parseDecimal(s string) decimal.Decimal {
	if strings.TrimSpace(s) == "" {
		return decimal.Zero()
	}
	value, err := decimal.Parse(s)
	if err != nil {
		return decimal.Zero()
	}
	return value
}
