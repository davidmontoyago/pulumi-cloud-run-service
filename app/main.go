package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog/log"
)

func main() {
	router := mux.NewRouter().StrictSlash(true)

	router.HandleFunc("/api/v1", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		json.NewEncoder(w).Encode(map[string][]string{
			"ranking":    {"nacho", "gandalf"},
			"dimensions": {"actitud", "leadership", "technical proef"},
		})
	}).Methods("GET")

	srv := &http.Server{
		Handler: router,
		// TODO make configurable
		Addr:         ":8080",
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  5 * time.Second,
	}

	log.Fatal().Err(srv.ListenAndServe())
}
