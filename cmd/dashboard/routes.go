package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/theadell/dns-api/ui"
)

func (app *App) Routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(app.sessionManager.LoadAndSave)

	fs := http.FileServer(http.FS(ui.StatifFS))
	r.Handle("/static/*", fs)

	// public routes
	r.Group(func(r chi.Router) {
		r.Use(app.redirectIfLoggedIn)
		r.Get("/", app.IndexHandler)
		r.Get("/login", app.initiateOAuthProcess)
		r.Get("/oauth/callback", app.handleOAuthCallback)
	})

	// protected routes
	r.Group(func(r chi.Router) {
		r.Use(app.RequireAuthentication)

		r.Route("/dashboard", func(r chi.Router) {
			r.Get("/", app.DashboardHandler)
			r.Post("/config/nginx", app.configHandler)
			r.Put("/config/nginx", app.configAdjusterHandler)
		})

		r.Route("/records", func(r chi.Router) {
			r.Post("/", app.AddRecordHandler)
			r.Delete("/", app.DeleteRecordHandler)
		})
	})

	r.NotFound(app.notFoundHandler)

	return r
}
