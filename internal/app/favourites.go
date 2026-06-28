package app

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoFavourites persists each user's favourite coins in MongoDB so they follow
// the user across clients (web + Telegram mini app) and survive restarts. It
// implements api.FavouritesStore. One document per subject: {_id, symbols}.
type mongoFavourites struct {
	coll *mongodriver.Collection
}

func newMongoFavourites(coll *mongodriver.Collection) *mongoFavourites {
	return &mongoFavourites{coll: coll}
}

type favouritesDoc struct {
	Subject string   `bson:"_id"`
	Symbols []string `bson:"symbols"`
}

func (m *mongoFavourites) Get(ctx context.Context, subject string) ([]string, error) {
	var doc favouritesDoc
	err := m.coll.FindOne(ctx, bson.M{"_id": subject}).Decode(&doc)
	if err == mongodriver.ErrNoDocuments {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return doc.Symbols, nil
}

func (m *mongoFavourites) Set(ctx context.Context, subject string, symbols []string) error {
	_, err := m.coll.UpdateOne(ctx,
		bson.M{"_id": subject},
		bson.M{"$set": bson.M{"symbols": symbols}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}
