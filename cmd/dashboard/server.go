package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

func (app *App) RunServer() {
	server := http.Server{
		Addr:         fmt.Sprintf("%s:%d", app.config.Host, app.config.Port),
		Handler:      app.sessionManager.LoadAndSave(app.Routes()),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	fmt.Println(server.Addr)
	err := server.ListenAndServe()
	log.Fatal(err)
}
