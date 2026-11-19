package dockui

import (
	"cmp"
	"fmt"
	"net/url"
	"strings"
)

// ConnectLine is one way to reach a container from the laptop.
type ConnectLine struct {
	Label string // "postgres", "management UI", "port"
	Value string // what gets copied
	Note  bool   // Value says why there's no laptop address; nothing to copy
}

// imageKind names the known service an image runs: "postgres", "mysql", ...
// or "" for anything else.
func imageKind(image string) string {
	ref := strings.ToLower(image)
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		ref = ref[:i]
	}
	base := ref[strings.LastIndexByte(ref, '/')+1:]
	switch {
	case base == "postgres" || base == "postgis" || strings.HasPrefix(base, "timescaledb") || base == "pgvector":
		return "postgres"
	case base == "mysql" || base == "mariadb" || base == "mysql-server":
		return "mysql"
	case base == "redis" || base == "valkey" || base == "redis-stack-server" || base == "redis-stack":
		return "redis"
	case base == "mongo" || base == "mongodb" || base == "mongodb-community-server":
		return "mongo"
	case base == "rabbitmq":
		return "rabbitmq"
	}
	return ""
}

// publicFor finds the published address for a private port, preferring one
// reachable from the laptop. The one it returns may be VM only.
func publicFor(addrs []Address, private int) (Address, bool) {
	var found Address
	ok := false
	for _, a := range addrs {
		if a.Private == private && (a.Proto == "" || a.Proto == "tcp") {
			if !ok || (found.VMOnly && !a.VMOnly) {
				found, ok = a, true
			}
		}
	}
	return found, ok
}

func userinfo(user, pass string) string {
	switch {
	case user == "" && pass == "":
		return ""
	case pass == "":
		return url.User(user).String() + "@"
	}
	return url.UserPassword(user, pass).String() + "@"
}

// Connect returns how to reach a container from the laptop: a connection URL
// for known images (credentials read from env), then every published address.
// The first line is the one to copy.
func Connect(image string, env map[string]string, addrs []Address) []ConnectLine {
	var lines []ConnectLine
	add := func(label, value string) { lines = append(lines, ConnectLine{Label: label, Value: value}) }
	// laptop returns the laptop address for a private port, or adds a note
	// saying why there isn't one: the port isn't published, or it's published
	// on the VM's localhost only (ports: ["127.0.0.1:5432:5432"]).
	laptop := func(kind string, port int) (Address, bool) {
		a, ok := publicFor(addrs, port)
		var why string
		switch {
		case !ok:
			why = fmt.Sprintf("port %d isn't published; add ports: [\"<host>:%d\"] to reach it", port, port)
		case a.VMOnly:
			why = fmt.Sprintf("port %d is published on the VM's localhost only; use ports: [\"%d:%d\"] to reach it from the laptop", port, a.Public, port)
		default:
			return a, true
		}
		lines = append(lines, ConnectLine{Label: kind, Value: why, Note: true})
		return Address{}, false
	}

	switch imageKind(image) {
	case "postgres":
		if a, ok := laptop("postgres", 5432); ok {
			user := cmp.Or(env["POSTGRES_USER"], "postgres")
			db := cmp.Or(env["POSTGRES_DB"], user)
			add("postgres", "postgres://"+userinfo(user, env["POSTGRES_PASSWORD"])+a.HostPort()+"/"+url.PathEscape(db))
		}
	case "mysql":
		if a, ok := laptop("mysql", 3306); ok {
			user := cmp.Or(env["MYSQL_USER"], env["MARIADB_USER"])
			pass := cmp.Or(env["MYSQL_PASSWORD"], env["MARIADB_PASSWORD"])
			if user == "" {
				user, pass = "root", cmp.Or(env["MYSQL_ROOT_PASSWORD"], env["MARIADB_ROOT_PASSWORD"])
			}
			db := cmp.Or(env["MYSQL_DATABASE"], env["MARIADB_DATABASE"])
			add("mysql", "mysql://"+userinfo(user, pass)+a.HostPort()+"/"+url.PathEscape(db))
		}
	case "redis":
		if a, ok := laptop("redis", 6379); ok {
			add("redis", "redis://"+userinfo("", cmp.Or(env["REDIS_PASSWORD"], env["VALKEY_PASSWORD"]))+a.HostPort())
		}
	case "mongo":
		if a, ok := laptop("mongo", 27017); ok {
			user, pass := env["MONGO_INITDB_ROOT_USERNAME"], env["MONGO_INITDB_ROOT_PASSWORD"]
			u := "mongodb://" + userinfo(user, pass) + a.HostPort() + "/" + url.PathEscape(env["MONGO_INITDB_DATABASE"])
			if user != "" {
				u += "?authSource=admin"
			}
			add("mongo", u)
		}
	case "rabbitmq":
		user := cmp.Or(env["RABBITMQ_DEFAULT_USER"], "guest")
		pass := cmp.Or(env["RABBITMQ_DEFAULT_PASS"], "guest")
		if a, ok := publicFor(addrs, 15672); ok && !a.VMOnly {
			add("management UI", "http://"+a.HostPort()+"  ("+user+" / "+pass+")")
		}
		if a, ok := publicFor(addrs, 5672); ok && !a.VMOnly {
			add("amqp", "amqp://"+userinfo(user, pass)+a.HostPort())
		}
		if len(lines) == 0 {
			laptop("amqp", 5672) // adds the note
		}
	}
	for _, a := range addrs {
		label := "port " + fmt.Sprint(a.Private)
		if a.VMOnly {
			label += " (VM only)"
		}
		add(label, a.HostPort())
	}
	return lines
}

// CopyValue is the part of a line to put on the clipboard: the URL without
// any note after it.
func (l ConnectLine) CopyValue() string {
	v, _, _ := strings.Cut(l.Value, "  (")
	return v
}

// Kind names the service an image runs ("postgres", "mysql", "redis", "mongo",
// "rabbitmq"), or "" when it isn't one ds knows how to connect to.
func Kind(image string) string { return imageKind(image) }
