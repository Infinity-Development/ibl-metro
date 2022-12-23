package main

import (
	"net/http"

	"github.com/joho/godotenv"
)

type MetroIBLAdapter struct{}

func main() {
	godotenv.Load()

	integrase.Init()

	http.ListenAndServe(":6821")
}
