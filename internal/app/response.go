package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const maxJSONBody = int64(1024 * 1024)

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	message := http.StatusText(status)
	if err != nil && err.Error() != "" {
		message = err.Error()
	}
	writeJSON(w, status, apiError{Error: message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeJSONValue(w, r, target, true)
}

func decodeJSONAllowUnknown(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeJSONValue(w, r, target, false)
}

func decodeJSONValue(
	w http.ResponseWriter,
	r *http.Request,
	target any,
	rejectUnknown bool,
) error {
	reader := http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(reader)
	if rejectUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}
