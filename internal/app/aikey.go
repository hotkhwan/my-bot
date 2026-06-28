package app

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bottrade/internal/api"
)

// mongoAISecrets persists per-user AI keys (sealed) in MongoDB, keyed by JWT
// subject (_id). Implements api.AISecretStore. Only the sealed ciphertext is
// stored — never plaintext.
type mongoAISecrets struct {
	coll *mongodriver.Collection
}

func newMongoAISecrets(coll *mongodriver.Collection) *mongoAISecrets {
	return &mongoAISecrets{coll: coll}
}

func (m *mongoAISecrets) Get(ctx context.Context, subject string) (api.AISecretDoc, bool, error) {
	var doc api.AISecretDoc
	err := m.coll.FindOne(ctx, bson.D{{Key: "_id", Value: subject}}).Decode(&doc)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return api.AISecretDoc{}, false, nil
	}
	if err != nil {
		return api.AISecretDoc{}, false, err
	}
	return doc, true, nil
}

func (m *mongoAISecrets) Save(ctx context.Context, doc api.AISecretDoc) error {
	_, err := m.coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: doc.Subject}}, doc, options.Replace().SetUpsert(true))
	return err
}

func (m *mongoAISecrets) Delete(ctx context.Context, subject string) error {
	_, err := m.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: subject}})
	return err
}
