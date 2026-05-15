package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
	_ "github.com/lib/pq"
)

type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

var db *sql.DB

var (
    requestsTotal = prometheus.NewCounter(
        prometheus.CounterOpts{
            Name: "app_requests_total",
            Help: "Total HTTP requests",
        },
    )

    errorsTotal = prometheus.NewCounter(
        prometheus.CounterOpts{
            Name: "app_errors_total",
            Help: "Total HTTP errors",
        },
    )

    requestDuration = prometheus.NewHistogram(
        prometheus.HistogramOpts{
            Name:    "app_request_duration_seconds",
            Help:    "Request duration",
            Buckets: prometheus.DefBuckets,
        },
    )
)

func metricsMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()

        requestsTotal.Inc()

        rw := &responseWriter{ResponseWriter: w, statusCode: 200}

        next.ServeHTTP(rw, r)

        duration := time.Since(start).Seconds()
        requestDuration.Observe(duration)

        if rw.statusCode >= 400 {
            errorsTotal.Inc()
        }
    })
}

type responseWriter struct {
    http.ResponseWriter
    statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
    rw.statusCode = code
    rw.ResponseWriter.WriteHeader(code)
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=postgres dbname=cruddb sslmode=disable"
	}

	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Println("warning: cannot connect to db:", err)
	}

	db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id    SERIAL PRIMARY KEY,
		name  TEXT NOT NULL,
		email TEXT NOT NULL UNIQUE
	)`)

	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(errorsTotal)
	prometheus.MustRegister(requestDuration)

	mux := http.NewServeMux()
	mux.HandleFunc("/users", usersHandler)
	mux.HandleFunc("/users/", userHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/metrics", promhttp.Handler())

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", metricsMiddleware(mux)))
}



func usersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query("SELECT id, name, email FROM users ORDER BY id")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var users []User
		for rows.Next() {
			var u User
			rows.Scan(&u.ID, &u.Name, &u.Email)
			users = append(users, u)
		}
		json.NewEncoder(w).Encode(users)

	case http.MethodPost:
		var u User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		err := db.QueryRow(
			"INSERT INTO users(name, email) VALUES($1,$2) RETURNING id",
			u.Name, u.Email,
		).Scan(&u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(u)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/users/")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}

	switch r.Method {
	case http.MethodGet:
		var u User
		err := db.QueryRow("SELECT id, name, email FROM users WHERE id=$1", id).
			Scan(&u.ID, &u.Name, &u.Email)
		if err == sql.ErrNoRows {
			http.Error(w, "not found", 404)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(u)

	case http.MethodPut:
		var u User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		res, err := db.Exec(
			"UPDATE users SET name=$1, email=$2 WHERE id=$3",
			u.Name, u.Email, id,
		)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			http.Error(w, "not found", 404)
			return
		}
		u.ID = id
		json.NewEncoder(w).Encode(u)

	case http.MethodDelete:
		res, err := db.Exec("DELETE FROM users WHERE id=$1", id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			http.Error(w, "not found", 404)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
