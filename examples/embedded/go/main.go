package main

import (
	"fmt"
	"log"
	"os"
	"time"

	kvlite "github.com/webong/kvlite"
)

type user struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	path := os.Getenv("KVLITE_DB_PATH")
	if path == "" {
		path = "./data"
	}
	driver := os.Getenv("KVLITE_DRIVER")
	if driver == "" {
		driver = "leveldb"
	}

	db, err := kvlite.Open(path, kvlite.WithDriver(driver))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Put("user:101", user{ID: 101, Name: "Ada"}, time.Hour); err != nil {
		log.Fatal(err)
	}
	var stored user
	if err := db.Get("user:101", &stored); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("user: %+v\n", stored)

	if err := db.PutBytes([]byte("blob:101"), []byte{0, 1, 2}, 0); err != nil {
		log.Fatal(err)
	}
	blob, err := db.GetBytes([]byte("blob:101"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("bytes: %v\n", blob)
}
