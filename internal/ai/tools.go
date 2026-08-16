package ai

type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	When        string `json:"when"`
	Arg         string `json:"arg,omitempty"`
}

func t(name, desc, when, arg string) ToolSpec {
	return ToolSpec{Name: name, Description: desc, When: when, Arg: arg}
}

var RoomTools = []ToolSpec{
	t("project_detail", "This room's disk, quota, image, ports vs host CPU/RAM/disk.", "User asks how much this project uses or to compare with the VPS.", "optional"),
	t("host_stats", "Live host CPU, RAM, disk, load.", "Compare this room with the whole VPS.", ""),
	t("list_containers", "Containers in this room.", "User asks which containers are running here.", "room id if needed"),
	t("list_images", "Images attached to this room.", "User asks which images this room has.", ""),
	t("list_volumes", "Volumes in this room.", "User asks about volumes.", ""),
}

var UsageTools = []ToolSpec{
	t("list_rooms", "Every room: name, id, status, disk used, quota.", "List or browse projects/rooms. Use this — never invent names.", ""),
	t("project_detail", "One room vs host totals.", "Details for a named room.", "room name or id"),
	t("host_stats", "Host CPU/RAM/disk/load snapshot.", "General usage, pressure, is the VPS high?", ""),
	t("get_cpu", "CPU percent, cores, load1.", "CPU-only questions.", ""),
	t("get_ram", "RAM used/total/percent.", "RAM-only questions.", ""),
	t("get_storage", "Disk and quota allocation.", "Disk-only questions.", ""),
	t("get_docker_status", "Whether Docker is up.", "Is Docker working?", ""),
	t("docker_ps", "All containers the panel knows.", "What is running on Docker?", ""),
	t("vps_logs", "Recent panel/host log excerpt.", "What just happened on the panel?", "panel|api|host"),
}

var HostTools = UsageTools

var TokenTools = []ToolSpec{
	t("list_rooms", "Live room names and ids on this VPS right now.", "User asks to list, show, or browse rooms. Call once, then answer from TOOL JSON. Never say Terminal.", ""),
	t("docs_token", "How to create a panel API token.", "How do I create a token / copy API?", ""),
	t("docs_github", "GitHub Action YAML (single vs multi) and ROOM_ID.", "GitHub workflow / deploy from repo.", ""),
	t("docs_update", "How to update an existing room via tar or GitHub.", "How do I update a room?", ""),
	t("docs_create_room", "How to create an empty room (API + panel).", "Empty room / new project room.", ""),
	t("docs_full", "Full public API brief.", "User wants the whole API.", ""),
}

var LogsTools = []ToolSpec{
	t("vps_logs", "Load a log kind excerpt.", "After they pick Panel/API/Deploy/Host, or if you set log_kind.", "panel|api|deploy|host"),
}

var ManagerTools = UsageTools

var DeployTools = []ToolSpec{
	t("list_rooms", "Existing rooms so you can update by id instead of creating a duplicate.", "Before a new deploy, or when they name a project to update.", ""),
	t("host_stats", "Free disk / host snapshot.", "Need free GB before quota.", ""),
}
