// Command tokenservice serves the token service: POST /token issues a signed token for a
// subject and POST /verify checks one. The signing secret comes from TOKENSERVICE_SECRET;
// the audience and listen address have fixed defaults.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"example.com/tokenservice/internal/auth"
	"example.com/tokenservice/internal/server"
)

const (
	defaultAddr     = ":8080"
	defaultAudience = "tokenservice"
	defaultTTL      = 15 * time.Minute
)

func main() {
	secret := os.Getenv("TOKENSERVICE_SECRET")
	issuer, err := auth.NewIssuer([]byte(secret), defaultAudience, defaultTTL)
	if err != nil {
		log.Fatalf("tokenservice: %v", err)
	}
	httpServer := &http.Server{
		Addr:              defaultAddr,
		Handler:           server.New(issuer, nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("tokenservice: listening on %s", defaultAddr)
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("tokenservice: %v", err)
	}
}
