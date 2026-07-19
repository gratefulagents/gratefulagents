package main

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestShouldProcessSlackEventClaim(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "new claim", want: true},
		{name: "duplicate", err: pgx.ErrNoRows, want: false},
		{name: "transient database error after ack", err: errors.New("database unavailable"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldProcessSlackEventClaim(tt.err); got != tt.want {
				t.Fatalf("shouldProcessSlackEventClaim(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
