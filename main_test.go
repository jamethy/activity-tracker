package main

import (
	"reflect"
	"testing"
	"time"
)

func Test_fillInDates(t *testing.T) {
	today := time.Date(2024, 6, 17, 11, 4, 0, 0, time.UTC)

	// just saves some space
	past14Days := []DayLog{
		{Date: time.Date(2024, 6, 17, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 13, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 12, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 11, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 9, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 8, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 6, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 5, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2024, 6, 4, 0, 0, 0, 0, time.UTC)},
	}

	type args struct {
		dayLogs []DayLog
		upTo    time.Time
	}
	tests := []struct {
		name string
		args args
		want []DayLog
	}{
		{
			name: "empty",
			args: args{
				dayLogs: nil,
				upTo:    today,
			},
			want: past14Days,
		},
		{
			name: "just-today",
			args: args{
				dayLogs: []DayLog{
					{Date: time.Date(2024, 6, 17, 0, 0, 0, 0, time.UTC)},
				},
				upTo: today,
			},
			want: past14Days,
		},
		{
			name: "just-yesterday",
			args: args{
				dayLogs: []DayLog{
					{Date: time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC)},
				},
				upTo: today,
			},
			want: past14Days,
		},
		{
			name: "couple-days",
			args: args{
				dayLogs: []DayLog{
					{Date: time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC)},
					{Date: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)},
				},
				upTo: today,
			},
			want: past14Days,
		},
		{
			name: "gap",
			args: args{
				dayLogs: []DayLog{
					{Date: time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC)},
					{Date: time.Date(2024, 6, 14, 0, 0, 0, 0, time.UTC)},
				},
				upTo: today,
			},
			want: past14Days,
		},
		{
			name: "past-14-days",
			args: args{
				dayLogs: []DayLog{
					{Date: time.Date(2024, 6, 2, 0, 0, 0, 0, time.UTC)},
				},
				upTo: today,
			},
			want: append(past14Days,
				DayLog{Date: time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC)},
				DayLog{Date: time.Date(2024, 6, 2, 0, 0, 0, 0, time.UTC)},
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fillInDates(tt.args.dayLogs, tt.args.upTo); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fillInDates() = %v, want %v", got, tt.want)
			}
		})
	}
}
