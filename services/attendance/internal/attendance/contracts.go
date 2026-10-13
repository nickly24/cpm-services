package attendance

// These shapes describe the public JSON views, independent of own persistence.
type AvailableCalendar struct {
	DivisionID   int64  `json:"division_id"`
	DivisionName string `json:"division_name"`
	Visibility   string `json:"visibility"`
	Timezone     string `json:"timezone"`
	IsMember     bool   `json:"is_member"`
	CanEdit      bool   `json:"can_edit"`
}
type ScheduleView struct {
	OrganizationID int64               `json:"organization_id"`
	Calendars      []AvailableCalendar `json:"calendars"`
	Events         []Event             `json:"events"`
	Clusters       []Overlap           `json:"overlap_clusters"`
}
type JournalView struct {
	OrganizationID int64          `json:"organization_id"`
	DivisionID     int64          `json:"division_id"`
	Days           []Day          `json:"days"`
	Entries        []Entry        `json:"entries"`
	Counts         map[string]int `json:"counts_by_type_code"`
	UnmarkedAbsent bool           `json:"unmarked_is_absent"`
}
type BatchResult struct {
	IDs     []int64 `json:"created_ids"`
	Updated int     `json:"updated_count"`
	Deleted int     `json:"deleted_count"`
}
type Deleted struct {
	Deleted bool `json:"deleted"`
}
type ZapDetail struct {
	Zap        Zap `json:"zap"`
	ImageCount int `json:"image_count"`
}
type QRResult struct {
	QR      string `json:"qr"`
	Expires int64  `json:"expires_at"`
}
type AuditInfo struct {
	Revision int64  `json:"revision"`
	Actor    int64  `json:"actor_account_id"`
	Action   string `json:"action"`
	At       string `json:"created_at"`
}
type ScheduleReferences struct {
	Groups   []Group           `json:"groups"`
	Teachers []Member          `json:"teachers"`
	Schools  []SchoolReference `json:"schools"`
	Rooms    []Room            `json:"rooms"`
}

func outputShape(kind, method string) any {
	switch kind {
	case "command-receipt":
		return CommandReceipt{}
	case "calendars":
		return []AvailableCalendar{}
	case "personal", "overlay", "schedule":
		return ScheduleView{}
	case "calendar":
		return Calendar{}
	case "rooms":
		return []Room{}
	case "room":
		return Room{}
	case "batch", "lesson":
		return BatchResult{}
	case "types":
		return []Type{}
	case "type":
		return Type{}
	case "days":
		return []Day{}
	case "day":
		if method == "delete" {
			return Deleted{}
		}
		return Day{}
	case "mark", "scan":
		return Entry{}
	case "unmark":
		return Deleted{}
	case "entries", "report":
		return JournalView{}
	case "own-report":
		return []JournalView{}
	case "students":
		return []Member{}
	case "schedule-references":
		return ScheduleReferences{}
	case "zaps":
		return []Zap{}
	case "zap":
		return ZapDetail{}
	case "zap-create", "zap-process", "zap-retry", "zap-unlink":
		return Zap{}
	case "qr":
		return QRResult{}
	case "audit":
		return []AuditInfo{}
	}
	return struct{}{}
}
