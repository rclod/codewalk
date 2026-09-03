package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	"example.com/checkout/internal/orders"
	"example.com/checkout/internal/outbox"
	"example.com/checkout/internal/payments"
	"example.com/checkout/internal/store"
)

func main() {
	db, err := sql.Open("sqlite", "checkout.db")
	if err != nil {
		log.Fatal(err)
	}
	worker := orders.NewWorker(
		outbox.New(db),
		store.New(db),
		payments.NewClient("https://payments.example.com"),
		2*time.Second,
	)
	log.Println("completion worker started")
	worker.Run(context.Background())
}
