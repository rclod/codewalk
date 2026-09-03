package main

import (
	"database/sql"
	"log"
	"net/http"

	"example.com/checkout/internal/orders"
	"example.com/checkout/internal/store"
)

func main() {
	db, err := sql.Open("sqlite", "checkout.db")
	if err != nil {
		log.Fatal(err)
	}
	service := orders.NewService(store.New(db))
	handler := orders.NewHandler(service)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", handler.Create)
	log.Fatal(http.ListenAndServe(":8080", mux))
}
