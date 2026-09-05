package config

import (
	"bytes"
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
	RememberSearches             bool   `json:"remember_searches"`
	SearchHistoryLimit           int    `json:"search_history_limit" validate:"min=0"`
	RememberFilters              bool   `json:"remember_filters"`
	FilterHistoryLimit           int    `json:"filter_history_limit" validate:"min=0"`
	DefaultFilter                string `json:"default_filter"`
	WishlistIntervalMinutes      int    `json:"wishlist_interval_minutes" validate:"min=0,max=525600"`
	WishlistNotifications        bool   `json:"wishlist_notifications"`
	RespondToIncomingSearches    bool   `json:"respond_to_incoming_searches"`
	MinimumIncomingSearchLength  int    `json:"minimum_incoming_search_length" validate:"min=0,max=50"`
	MaximumIncomingSearchResults int    `json:"maximum_incoming_search_results" validate:"min=50,max=10000"`
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

type BandwidthProfile struct {
	Name                  string `json:"name"`
	UploadSpeedLimitKiB   int    `json:"upload_speed_limit_kib"`
	DownloadSpeedLimitKiB int    `json:"download_speed_limit_kib"`
}

type Bandwidth struct {
	Profiles      []BandwidthProfile `json:"profiles"`
	ActiveProfile string             `json:"active_profile"`
}

type Uploads struct {
	AutoClearCompleted bool             `json:"auto_clear_completed"`
	LimitScope         UploadLimitScope `json:"limit_scope"`
	Scheduling         UploadScheduling `json:"scheduling"`
}

type legacyUploadProfile struct {
	Name          string `json:"name"`
	SpeedLimitKiB int    `json:"speed_limit_kib"`
}

func defaultBandwidth() Bandwidth {
	return Bandwidth{Profiles: []BandwidthProfile{{Name: "Unlimited"}}, ActiveProfile: "Unlimited"}
}

func (b Bandwidth) ActiveProfileLimits() BandwidthProfile {
	for _, profile := range b.Profiles {
		if profile.Name == b.ActiveProfile {
			return profile
		}
	}
	return BandwidthProfile{}
}

type Downloads struct {
	AutoClearCompleted  bool   `json:"auto_clear_completed"`
	AfterFileCommand    string `json:"after_file_command"`
	AfterFolderCommand  string `json:"after_folder_command"`
	FileNotifications   bool   `json:"file_notifications"`
	FolderNotifications bool   `json:"folder_notifications"`
}

func ValidateBandwidthProfileName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid bandwidth profile name %q", name)
	}
	return nil
}

func validateBandwidth(b Bandwidth) error {
	if len(b.Profiles) == 0 {
		return errors.New("config: at least one bandwidth profile is required")
	}
	active := false
	for i, profile := range b.Profiles {
		if err := ValidateBandwidthProfileName(profile.Name); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		for _, previous := range b.Profiles[:i] {
			if strings.EqualFold(previous.Name, profile.Name) {
				return fmt.Errorf("config: duplicate bandwidth profile name %q", profile.Name)
			}
		}
		if profile.UploadSpeedLimitKiB < 0 || profile.UploadSpeedLimitKiB > 1000000 || profile.DownloadSpeedLimitKiB < 0 || profile.DownloadSpeedLimitKiB > 1000000 {
			return fmt.Errorf("config: invalid speed limit for bandwidth profile %q", profile.Name)
		}
		active = active || profile.Name == b.ActiveProfile
	}
	if !active {
		return fmt.Errorf("config: active bandwidth profile %q does not exist", b.ActiveProfile)
	}
	return nil
}

type Config struct {
	Soulseek        Soulseek  `json:"soulseek"`
	Search          Search    `json:"search"`
	Bandwidth       Bandwidth `json:"bandwidth"`
	Uploads         Uploads   `json:"uploads"`
	Downloads       Downloads `json:"downloads"`
	DownloadDir     string    `json:"download_dir" validate:"required"`
	Shares          []Share   `json:"shares"`
	ShareExclusions []string  `json:"share_exclusions"`
	DownloadSlots   int       `json:"download_slots" validate:"min=1"`
	UploadSlots     int       `json:"upload_slots" validate:"min=1"`
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
	Search          Search    `json:"search"`
	Bandwidth       Bandwidth `json:"bandwidth"`
	Uploads         Uploads   `json:"uploads"`
	Downloads       Downloads `json:"downloads"`
	DownloadDir     string    `json:"download_dir"`
	Shares          []Share   `json:"shares"`
	ShareExclusions []string  `json:"share_exclusions"`
	DownloadSlots   int       `json:"download_slots"`
	UploadSlots     int       `json:"upload_slots"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{ShareExclusions: DefaultShareExclusions(), Soulseek: Soulseek{Server: DefaultServer, ListenAddr: DefaultListenAddr, ConnectOnStartup: true, NATPMPPortMapping: true, UPnPPortMapping: true}, Search: Search{RememberSearches: true, SearchHistoryLimit: 200, RememberFilters: true, FilterHistoryLimit: 50, WishlistIntervalMinutes: 15, WishlistNotifications: true, RespondToIncomingSearches: true, MinimumIncomingSearchLength: 3, MaximumIncomingSearchResults: 300}, Bandwidth: defaultBandwidth(), Uploads: Uploads{LimitScope: UploadLimitTotal, Scheduling: UploadSchedulingFIFO}, Downloads: Downloads{FolderNotifications: true}, DownloadDir: filepath.Join(home, DefaultDownloadDir), DownloadSlots: 4, UploadSlots: 2}
}

func (c Config) Redacted() SafeConfig {
	var out SafeConfig
	out.Soulseek.Username, out.Soulseek.Password, out.Soulseek.Server, out.Soulseek.ListenAddr, out.Soulseek.NetworkInterface, out.Soulseek.ConnectOnStartup, out.Soulseek.NATPMPPortMapping, out.Soulseek.UPnPPortMapping = c.Soulseek.Username, "[redacted]", c.Soulseek.Server, c.Soulseek.ListenAddr, c.Soulseek.NetworkInterface, c.Soulseek.ConnectOnStartup, c.Soulseek.NATPMPPortMapping, c.Soulseek.UPnPPortMapping
	out.Search, out.Bandwidth, out.Uploads, out.Downloads, out.DownloadDir, out.Shares, out.DownloadSlots, out.UploadSlots = c.Search, c.Bandwidth, c.Uploads, c.Downloads, c.DownloadDir, append([]Share(nil), c.Shares...), c.DownloadSlots, c.UploadSlots
	out.Bandwidth.Profiles = append([]BandwidthProfile(nil), c.Bandwidth.Profiles...)
	out.ShareExclusions = append([]string{}, c.ShareExclusions...)
	return out
}

func (c Config) Validate() error {
	if _, err := NormalizeShareExclusions(c.ShareExclusions); err != nil {
		return err
	}
	if strings.IndexByte(c.Downloads.AfterFileCommand, 0) >= 0 || strings.IndexByte(c.Downloads.AfterFolderCommand, 0) >= 0 {
		return errors.New("config: download commands must not contain NUL bytes")
	}
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
	if err := validateBandwidth(c.Bandwidth); err != nil {
		return err
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

func (c *Config) UnmarshalJSON(data []byte) error {
	type plainConfig Config
	next := plainConfig(*c)
	next.Bandwidth = Bandwidth{}
	if err := json.Unmarshal(data, &next); err != nil {
		return err
	}
	var raw struct {
		Bandwidth json.RawMessage `json:"bandwidth"`
		Uploads   struct {
			Profiles      json.RawMessage `json:"profiles"`
			ActiveProfile json.RawMessage `json:"active_profile"`
		} `json:"uploads"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Bandwidth == nil {
		next.Bandwidth = defaultBandwidth()
		if raw.Uploads.Profiles != nil {
			var profiles []legacyUploadProfile
			if err := json.Unmarshal(raw.Uploads.Profiles, &profiles); err != nil {
				return fmt.Errorf("config: invalid legacy upload profiles: %w", err)
			}
			next.Bandwidth.Profiles = make([]BandwidthProfile, len(profiles))
			for i, profile := range profiles {
				next.Bandwidth.Profiles[i] = BandwidthProfile{Name: profile.Name, UploadSpeedLimitKiB: profile.SpeedLimitKiB}
			}
		}
		if raw.Uploads.ActiveProfile != nil {
			if bytes.Equal(bytes.TrimSpace(raw.Uploads.ActiveProfile), []byte("null")) {
				return errors.New("config: legacy active upload profile cannot be null")
			}
			if err := json.Unmarshal(raw.Uploads.ActiveProfile, &next.Bandwidth.ActiveProfile); err != nil {
				return err
			}
		}
	}
	if err := validateBandwidth(next.Bandwidth); err != nil {
		return err
	}
	*c = Config(next)
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
	c.ShareExclusions, err = NormalizeShareExclusions(c.ShareExclusions)
	if err != nil {
		return c, err
	}
	if missing && c.Soulseek.Username == "" && c.Soulseek.Password == "" {
		return c, nil
	}
	return c, c.Validate()
}

func (c Config) Save(path string) error {
	rules, err := NormalizeShareExclusions(c.ShareExclusions)
	if err != nil {
		return err
	}
	c.ShareExclusions = rules
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
