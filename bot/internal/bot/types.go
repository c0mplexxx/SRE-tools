package bot

import "strings"

const (
	TenantOne                   = "1"
	DefaultTelegramMessageLimit = 4096
)

type Alert struct {
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
}

func (a Alert) label(name string) string {
	return strings.TrimSpace(a.Labels[name])
}

func (a Alert) annotation(name string) string {
	return strings.TrimSpace(a.Annotations[name])
}

type InstanceCheck struct {
	Tenant           string
	Instance         string
	Window           string
	Up               *float64
	CPUUsagePercent  *float64
	CPUCores         *float64
	Load1            *float64
	Load5            *float64
	Load15           *float64
	MemoryPercent    *float64
	MemoryUsedBytes  *float64
	MemoryTotalBytes *float64
	DiskUsage        []MetricValue
	DiskIOBusy       []MetricValue
	NetworkReceive   []MetricValue
	NetworkTransmit  []MetricValue
}

type MetricValue struct {
	Name  string
	Value float64
}

type Chat struct {
	ID int64 `json:"id"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Message struct {
	Chat Chat   `json:"chat"`
	From *User  `json:"from"`
	Text string `json:"text"`
}

type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}
