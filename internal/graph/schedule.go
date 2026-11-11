package graph

import "time"

// ScheduleInfo represents a Graph PIM activation schedule payload.
type ScheduleInfo struct {
	StartDateTime string             `json:"startDateTime"`
	Expiration    ScheduleExpiration `json:"expiration"`
}

// ScheduleExpiration specifies duration-based expiration for Graph PIM activation requests.
type ScheduleExpiration struct {
	Type     string `json:"type"`
	Duration string `json:"duration"`
}

// ActivationResponse captures the status field from Graph PIM schedule request responses.
type ActivationResponse struct {
	Status string `json:"status"`
}

// AfterDurationSchedule builds a ScheduleInfo starting at now (UTC) for durationISO.
func AfterDurationSchedule(now time.Time, durationISO string) ScheduleInfo {
	return ScheduleInfo{
		StartDateTime: now.UTC().Format(time.RFC3339),
		Expiration: ScheduleExpiration{
			Type:     "AfterDuration",
			Duration: durationISO,
		},
	}
}
