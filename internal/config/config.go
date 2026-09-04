package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-playground/validator/v10"
)

const (
	DefaultServer      = "server.slsknet.org:2242"
	DefaultListenAddr  = "0.0.0.0:50300"
	DefaultDownloadDir = "Downloads/oto"
)

type Soulseek struct {
	Username          string `json:"username" validate:"required"`
	Password          string `json:"password" validate:"required"`
	Server            string `json:"server" validate:"required"`
	ListenAddr        string `json:"listen_addr" validate:"required"`
	NetworkInterface  string `json:"network_interface"`
	ConnectOnStartup  bool   `json:"connect_on_startup"`
	NATPMPPortMapping bool   `json:"nat_pmp_port_mapping"`
	UPnPPortMapping   bool   `json:"upnp_port_mapping"`
}

type Share struct {
	Name string `json:"name" validate:"required"`
	Path string `json:"path" validate:"required"`
}

type Search struct {
	RememberSearches        bool `json:"remember_searches"`
	SearchHistoryLimit      int  `json:"search_history_limit" validate:"min=0"`
	RememberFilters         bool `json:"remember_filters"`
	FilterHistoryLimit      int  `json:"filter_history_limit" validate:"min=0"`
	WishlistIntervalMinutes int  `json:"wishlist_interval_minutes" validate:"min=0,max=525600"`
	WishlistNotifications   bool `json:"wishlist_notifications"`
}

type UploadLimitScope string

type UploadScheduling string

const (
	UploadLimitTotal       UploadLimitScope = "total"
	UploadLimitPerTransfer UploadLimitScope = "per_transfer"

	UploadSchedulingFIFO          UploadScheduling = "fifo"
	UploadSchedulingRoundRobin    UploadScheduling = "round_robin"
	UploadSchedulingRandom        UploadScheduling = "random"
	UploadSchedulingSmallestFirst UploadScheduling = "smallest_first"
)

type UploadProfile struct {
	Name          string `json:"name"`
	SpeedLimitKiB int    `json:"speed_limit_kib"`
}

type Uploads struct {
	Profiles      []UploadProfile  `json:"profiles"`
	ActiveProfile string           `json:"active_profile"`
	LimitScope    UploadLimitScope `json:"limit_scope"`
	Scheduling    UploadScheduling `json:"scheduling"`
}

func (u Uploads) ActiveSpeedLimitKiB() int {
	for _, profile := range u.Profiles {
		if profile.Name == u.ActiveProfile {
			return profile.SpeedLimitKiB
		}
	}
	return 0
}

func ValidateUploadProfileName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid upload profile name %q", name)
	}
	return nil
}

type Config struct {
	Soulseek      Soulseek `json:"soulseek"`
	Search        Search   `json:"search"`
	Uploads       Uploads  `json:"uploads"`
	DownloadDir   string   `json:"download_dir" validate:"required"`
	Shares        []Share  `json:"shares"`
	DownloadSlots int      `json:"download_slots" validate:"min=1"`
	UploadSlots   int      `json:"upload_slots" validate:"min=1"`
}

type SafeConfig struct {
	Soulseek struct {
		Username          string `json:"username"`
		Password          string `json:"-"`
		Server            string `json:"server"`
		ListenAddr        string `json:"listen_addr"`
		NetworkInterface  string `json:"network_interface"`
		ConnectOnStartup  bool   `json:"connect_on_startup"`
		NATPMPPortMapping bool   `json:"nat_pmp_port_mapping"`
		UPnPPortMapping   bool   `json:"upnp_port_mapping"`
	} `json:"soulseek"`
	Search        Search  `json:"search"`
	Uploads       Uploads `json:"uploads"`
	DownloadDir   string  `json:"download_dir"`
	Shares        []Share `json:"shares"`
	DownloadSlots int     `json:"download_slots"`
	UploadSlots   int     `json:"upload_slots"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{Soulseek: Soulseek{Server: DefaultServer, ListenAddr: DefaultListenAddr, ConnectOnStartup: true, NATPMPPortMapping: true, UPnPPortMapping: true}, Search: Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50, WishlistIntervalMinutes: 15, WishlistNotifications: true}, Uploads: Uploads{Profiles: []UploadProfile{{Name: "Unlimited"}}, ActiveProfile: "Unlimited", LimitScope: UploadLimitTotal, Scheduling: UploadSchedulingFIFO}, DownloadDir: filepath.Join(home, DefaultDownloadDir), DownloadSlots: 4, UploadSlots: 2}
}

func (c Config) Redacted() SafeConfig {
	var out SafeConfig
	out.Soulseek.Username, out.Soulseek.Password, out.Soulseek.Server, out.Soulseek.ListenAddr, out.Soulseek.NetworkInterface, out.Soulseek.ConnectOnStartup, out.Soulseek.NATPMPPortMapping, out.Soulseek.UPnPPortMapping = c.Soulseek.Username, "[redacted]", c.Soulseek.Server, c.Soulseek.ListenAddr, c.Soulseek.NetworkInterface, c.Soulseek.ConnectOnStartup, c.Soulseek.NATPMPPortMapping, c.Soulseek.UPnPPortMapping
	out.Search, out.Uploads, out.DownloadDir, out.Shares, out.DownloadSlots, out.UploadSlots = c.Search, c.Uploads, c.DownloadDir, append([]Share(nil), c.Shares...), c.DownloadSlots, c.UploadSlots
	out.Uploads.Profiles = append([]UploadProfile(nil), c.Uploads.Profiles...)
	return out
}

func (c Config) Validate() error {
	if err := validator.New().Struct(c); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	for label, address := range map[string]string{"server": c.Soulseek.Server, "listen address": c.Soulseek.ListenAddr} {
		_, portText, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("config: invalid %s: %w", label, err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 || label == "listen address" && port < 1024 {
			return fmt.Errorf("config: invalid %s port", label)
		}
	}
	if c.Uploads.LimitScope != UploadLimitTotal && c.Uploads.LimitScope != UploadLimitPerTransfer {
		return fmt.Errorf("config: invalid upload limit scope %q", c.Uploads.LimitScope)
	}
	switch c.Uploads.Scheduling {
	case UploadSchedulingFIFO, UploadSchedulingRoundRobin, UploadSchedulingRandom, UploadSchedulingSmallestFirst:
	default:
		return fmt.Errorf("config: invalid upload scheduling %q", c.Uploads.Scheduling)
	}
	if len(c.Uploads.Profiles) == 0 {
		return errors.New("config: at least one upload profile is required")
	}
	active, names := false, make([]string, 0, len(c.Uploads.Profiles))
	for _, profile := range c.Uploads.Profiles {
		if err := ValidateUploadProfileName(profile.Name); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		for _, name := range names {
			if strings.EqualFold(name, profile.Name) {
				return fmt.Errorf("config: duplicate upload profile name %q", profile.Name)
			}
		}
		names = append(names, profile.Name)
		if profile.SpeedLimitKiB < 0 || profile.SpeedLimitKiB > 1000000 {
			return fmt.Errorf("config: invalid speed limit for upload profile %q", profile.Name)
		}
		active = active || profile.Name == c.Uploads.ActiveProfile
	}
	if !active {
		return fmt.Errorf("config: active upload profile %q does not exist", c.Uploads.ActiveProfile)
	}
	seen := map[string]bool{}
	for _, sh := range c.Shares {
		if strings.TrimSpace(sh.Name) == "" || strings.ContainsAny(sh.Name, "/\\") || sh.Name == "." || sh.Name == ".." || seen[sh.Name] {
			return fmt.Errorf("config: invalid share %q", sh.Name)
		}
		if sh.Path == "" {
			return fmt.Errorf("config: empty path for share %q", sh.Name)
		}
		seen[sh.Name] = true
	}
	return nil
}

func applyEnv(c *Config) {
	for k, dst := range map[string]*string{"OTO_USERNAME": &c.Soulseek.Username, "OTO_PASSWORD": &c.Soulseek.Password, "OTO_SERVER": &c.Soulseek.Server, "OTO_LISTEN_ADDR": &c.Soulseek.ListenAddr, "OTO_NETWORK_INTERFACE": &c.Soulseek.NetworkInterface, "OTO_DOWNLOAD_DIR": &c.DownloadDir} {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			*dst = v
		}
	}
}

// Load reads JSON over defaults, then applies environment overrides.
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, err
		}
	} else if !missing {
		return c, err
	}
	applyEnv(&c)
	if missing && c.Soulseek.Username == "" && c.Soulseek.Password == "" {
		return c, nil
	}
	return c, c.Validate()
}

func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if _, fromEnv := os.LookupEnv("OTO_PASSWORD"); fromEnv {
		c.Soulseek.Password = ""
	}
	return SaveJSON(path, c)
}

// SaveJSON writes a private JSON file atomically.
func SaveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0700)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err == nil {
		ok = true
		_ = os.Chmod(path, 0600)
	}
	return err
}

func xdg(env, suffix string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	h, err := os.UserHomeDir()
	if err != nil {
		if u, e := user.Current(); e == nil {
			h = u.HomeDir
		}
	}
	return filepath.Join(h, suffix)
}

func ConfigPath() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "oto", "config.json")
}
func DataDir() string {
	return filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(".local", "state")), "oto")
}
func StatePath() string   { return filepath.Join(DataDir(), "downloads.json") }
func HistoryPath() string { return filepath.Join(DataDir(), "history.json") }
func SocketPath() string {
	if p := os.Getenv("XDG_RUNTIME_DIR"); p != "" {
		return filepath.Join(p, "oto", "oto.sock")
	}
	return filepath.Join(os.TempDir(), "oto-"+strconv.Itoa(os.Getuid()), "oto", "oto.sock")
}
