package bot

import (
	"strings"
	"time"
)

const (
	TenantOne                   = "1"
	TenantZero                  = "0"
	TenantNonZero               = "non-zero"
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

func isExplicitNonZeroTenant(tenant string) bool {
	tenant = strings.TrimSpace(tenant)
	return tenant != "" && tenant != TenantZero
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

type InstanceCoverage struct {
	Tenant     string
	Instance   string
	Alertnames []string
}

type GraphRange struct {
	Raw        string
	Duration   time.Duration
	Step       time.Duration
	RateWindow string
	Start      time.Time
	End        time.Time
}

type GraphTarget struct {
	Raw     string
	Matcher string
	Regex   bool
	Hosts   []string
	HostCap int
}

type InstanceGraph struct {
	Tenant       string
	Command      string
	Title        string
	Unit         string
	Instance     string
	Target       GraphTarget
	Range        GraphRange
	Series       []GraphSeries
	EmptyMessage string
}

type GraphSeries struct {
	Name   string
	Points []GraphPoint
}

type GraphPoint struct {
	Time  time.Time
	Value float64
	Valid bool
}

type MetricValue struct {
	Name   string
	Value  float64
	Labels map[string]string
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
