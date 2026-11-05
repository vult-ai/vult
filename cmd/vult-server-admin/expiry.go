package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

type expiryResult struct {
	Books   int  `json:"books"`
	Expired int  `json:"expired"`
	More    bool `json:"more"`
}

func expireDocuments(ctx context.Context, db *pgxpool.Pool, q request) (expiryResult, error) {
	var result expiryResult
	if q.BookID != "" && !ledger.ValidID(q.BookID) {
		return result, errOperation
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var operator bool
	if err := db.QueryRow(ctx, "SELECT public.ledger_import_owner() AND current_user=session_user").Scan(&operator); err != nil || !operator {
		return result, errOperation
	}
	storage, err := documents.OpenDisk(q.Directory)
	if err != nil {
		return result, errOperation
	}
	defer storage.Close()
	admin := ledger.Admin{DB: db}
	books := []string{q.BookID}
	if q.BookID == "" {
		books, err = admin.ExpiredDocumentBooks(ctx, 20)
		if err != nil {
			return result, errOperation
		}
	}
	for _, book := range books {
		count, err := admin.ExpireBookDocuments(ctx, book, storage, 100)
		if err != nil {
			return result, err
		}
		result.Books++
		result.Expired += count
	}
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.documents
 WHERE used_at IS NULL AND state<>'expired' AND expires_at<=clock_timestamp()
 AND($1='' OR book_id=nullif($1,'')::uuid))`, q.BookID).Scan(&result.More)
	return result, err
}
