// Package ssh provides SSH connection management and remote file operations for Grok Build Switch.
// Users can connect to remote servers and manage files through the web UI.
package ssh

import (
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ConnectionConfig stores SSH connection parameters (without secrets).
type ConnectionConfig struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	AuthType string `json:"auth_type"` // "key" or "password"
	KeyPath  string `json:"key_path,omitempty"`
}

// Connection extends config with runtime state.
type Connection struct {
	ConnectionConfig
	Client      *ssh.Client
	SFTP        *sftp.Client
	Connected   bool
	LastError   string    `json:"-"`
	ConnectedAt time.Time `json:"-"`
}

// FileInfo describes a remote file or directory.
type FileInfo struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// Manager manages a pool of SSH connections.
type Manager struct {
	mu          sync.Mutex
	connections map[string]*Connection
	dataDir     string
	configsFile string
}

// NewManager creates a new SSH manager.
func NewManager(dataDir string) *Manager {
	return &Manager{
		connections: make(map[string]*Connection),
		dataDir:     dataDir,
		configsFile: filepath.Join(dataDir, "ssh_connections.json"),
	}
}

// loadConfigs reads saved connection configs from disk.
func (m *Manager) loadConfigs() ([]ConnectionConfig, error) {
	raw, err := os.ReadFile(m.configsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var configs []ConnectionConfig
	if err := json.Unmarshal(raw, &configs); err != nil {
		return nil, fmt.Errorf("解析 SSH 连接配置失败: %w", err)
	}
	return configs, nil
}

// saveConfigs writes connection configs to disk.
func (m *Manager) saveConfigs() error {
	m.mu.Lock()
	configs := make([]ConnectionConfig, 0, len(m.connections))
	for _, conn := range m.connections {
		configs = append(configs, conn.ConnectionConfig)
	}
	m.mu.Unlock()

	if err := os.MkdirAll(m.dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.configsFile, raw, 0o600)
}

// ListConfigs returns all saved connection configs (without secrets).
func (m *Manager) ListConfigs() []ConnectionConfig {
	configs, err := m.loadConfigs()
	if err != nil {
		return nil
	}
	return configs
}

// AddConfig saves a new connection config.
func (m *Manager) AddConfig(cfg ConnectionConfig) error {
	if cfg.ID == "" {
		return fmt.Errorf("缺少连接 ID")
	}
	m.mu.Lock()
	m.connections[cfg.ID] = &Connection{ConnectionConfig: cfg}
	m.mu.Unlock()
	return m.saveConfigs()
}

// UpdateConfig updates an existing connection config.
func (m *Manager) UpdateConfig(cfg ConnectionConfig) error {
	m.mu.Lock()
	if _, ok := m.connections[cfg.ID]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("连接不存在")
	}
	m.connections[cfg.ID].ConnectionConfig = cfg
	m.mu.Unlock()
	return m.saveConfigs()
}

// DeleteConfig removes a saved connection config and disconnects if active.
func (m *Manager) DeleteConfig(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("缺少连接 ID")
	}
	m.mu.Lock()
	conn, ok := m.connections[id]
	if !ok {
		m.mu.Unlock()
		return os.ErrNotExist
	}
	conn.close()
	delete(m.connections, id)
	m.mu.Unlock()
	return m.saveConfigs()
}

// loadAuthMethod builds an ssh.AuthMethod from the connection config.
func loadAuthMethod(cfg ConnectionConfig, password string) (ssh.AuthMethod, error) {
	switch cfg.AuthType {
	case "key":
		keyPath := cfg.KeyPath
		if len(keyPath) > 0 && keyPath[0] == '~' {
			home, _ := os.UserHomeDir()
			keyPath = filepath.Join(home, keyPath[1:])
		}
		keyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("读取密钥失败: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			// Try parsing as encrypted key with empty passphrase, or return error.
			var keyErr *ssh.PassphraseMissingError
			if errors.As(err, &keyErr) {
				return nil, fmt.Errorf("密钥已加密，需要提供密码")
			}
			// Try parsing PEM manually for better error messages.
			block, _ := pem.Decode(keyBytes)
			if block == nil {
				return nil, fmt.Errorf("无效的密钥格式")
			}
			signer, err = ssh.ParsePrivateKey(keyBytes)
			if err != nil {
				return nil, fmt.Errorf("解析密钥失败: %w", err)
			}
		}
		return ssh.PublicKeys(signer), nil
	case "password":
		if password == "" {
			return nil, fmt.Errorf("需要密码")
		}
		return ssh.Password(password), nil
	default:
		return nil, fmt.Errorf("不支持的认证方式: %s", cfg.AuthType)
	}
}

// Connect establishes an SSH connection.
func (m *Manager) Connect(id, password string) error {
	m.mu.Lock()
	conn, ok := m.connections[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("连接不存在: %s", id)
	}

	auth, err := loadAuthMethod(conn.ConnectionConfig, password)
	if err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", conn.Host, conn.Port)
	if conn.Port == 0 {
		addr = fmt.Sprintf("%s:22", conn.Host)
	}

	config := &ssh.ClientConfig{
		User:            conn.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Users can switch to known_hosts later.
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return fmt.Errorf("SFTP 初始化失败: %w", err)
	}

	m.mu.Lock()
	conn.Client = client
	conn.SFTP = sftpClient
	conn.Connected = true
	conn.LastError = ""
	conn.ConnectedAt = time.Now()
	m.mu.Unlock()

	return nil
}

// Disconnect closes an SSH connection.
func (m *Manager) Disconnect(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn, ok := m.connections[id]
	if !ok {
		return fmt.Errorf("连接不存在: %s", id)
	}
	conn.close()
	return nil
}

// GetConnection returns an active connection by ID.
func (m *Manager) GetConnection(id string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn, ok := m.connections[id]
	if !ok {
		return nil, fmt.Errorf("连接不存在: %s", id)
	}
	if !conn.Connected {
		return nil, fmt.Errorf("未连接: %s", conn.Name)
	}
	return conn, nil
}

// ListActive returns all connections with their status.
func (m *Manager) ListActive() []ConnectionConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	configs := make([]ConnectionConfig, 0, len(m.connections))
	for _, conn := range m.connections {
		if conn.Connected {
			configs = append(configs, conn.ConnectionConfig)
		}
	}
	return configs
}

// ListDirectory lists files in a remote directory.
func (m *Manager) ListDirectory(connID, path string) ([]FileInfo, error) {
	conn, err := m.GetConnection(connID)
	if err != nil {
		return nil, err
	}
	entries, err := conn.SFTP.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}
	infos := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		fullPath := filepath.Join(path, entry.Name())
		if len(fullPath) > 0 && fullPath[0] != '/' {
			fullPath = "/" + fullPath
		}
		infos = append(infos, FileInfo{
			Name:    entry.Name(),
			Path:    fullPath,
			Size:    entry.Size(),
			Mode:    entry.Mode().String(),
			IsDir:   entry.IsDir(),
			ModTime: entry.ModTime(),
		})
	}
	return infos, nil
}

// DeleteFile deletes a remote file or empty directory.
func (m *Manager) DeleteFile(connID, path string) error {
	conn, err := m.GetConnection(connID)
	if err != nil {
		return err
	}
	info, err := conn.SFTP.Stat(path)
	if err != nil {
		return fmt.Errorf("文件不存在: %w", err)
	}
	if info.IsDir() {
		return conn.SFTP.RemoveDirectory(path)
	}
	return conn.SFTP.Remove(path)
}

// DeleteFiles deletes multiple files/directories.
func (m *Manager) DeleteFiles(connID string, paths []string) error {
	for _, path := range paths {
		if err := m.DeleteFile(connID, path); err != nil {
			return fmt.Errorf("删除 %s 失败: %w", path, err)
		}
	}
	return nil
}

// GetFileContent downloads a file's content for preview.
func (m *Manager) GetFileContent(connID, path string) ([]byte, error) {
	conn, err := m.GetConnection(connID)
	if err != nil {
		return nil, err
	}
	info, err := conn.SFTP.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("文件不存在: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("无法预览目录")
	}
	if info.Size() > 1<<20 { // 1 MB limit.
		return nil, fmt.Errorf("文件过大 (>1MB)，无法预览")
	}
	f, err := conn.SFTP.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()
	data := make([]byte, info.Size())
	_, err = f.Read(data)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	return data, nil
}

// SaveFileContent writes content back to a remote file.
func (m *Manager) SaveFileContent(connID, path string, content []byte) error {
	conn, err := m.GetConnection(connID)
	if err != nil {
		return err
	}
	f, err := conn.SFTP.Create(path)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	return nil
}

// close shuts down the SSH and SFTP clients.
func (c *Connection) close() {
	if c.SFTP != nil {
		c.SFTP.Close()
		c.SFTP = nil
	}
	if c.Client != nil {
		c.Client.Close()
		c.Client = nil
	}
	c.Connected = false
}

// IsTextFile heuristically checks if a file is likely a text file.
func IsTextFile(name string) bool {
	ext := filepath.Ext(name)
	switch ext {
	case ".txt", ".md", ".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".htm",
		".css", ".js", ".ts", ".jsx", ".tsx", ".py", ".go", ".rs", ".java", ".c",
		".cc", ".cpp", ".h", ".hpp", ".rb", ".php", ".sh", ".bash", ".zsh", ".ps1",
		".sql", ".log", ".env", ".ini", ".cfg", ".conf", ".csv", ".tsv":
		return true
	}
	return false
}

// FormatSize formats a byte count as a human-readable string.
func FormatSize(bytes int64) string {

	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// GenerateID creates a simple unique ID for a new connection.
func GenerateID() string {
	return fmt.Sprintf("ssh_%d", time.Now().UnixNano())
}

// ParseSSHConfig reads ~/.ssh/config and returns connection configs.
// VSCode Remote - SSH stores connections in this file.
func ParseSSHConfig() []ConnectionConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	return ParseSSHConfigBytes(data)
}

// ParseSSHConfigBytes parses SSH config from bytes (useful for testing).
func ParseSSHConfigBytes(data []byte) []ConnectionConfig {
	var configs []ConnectionConfig
	lines := strings.Split(string(data), "\n")

	var current *ConnectionConfig
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Split into key and value.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.ToLower(fields[0])
		value := strings.Join(fields[1:], " ")

		if key == "host" {
			// Save previous config.
			if current != nil && current.Host != "" {
				configs = append(configs, *current)
			}
			// Start new config. Use first alias as name and ID.
			aliases := strings.Fields(value)
			name := aliases[0]
			current = &ConnectionConfig{
				ID:       "sshcfg_" + name,
				Name:     name,
				Port:     22,
				AuthType: "key",
			}
		} else if current == nil {
			continue
		} else if key == "hostname" {
			current.Host = value
		} else if key == "user" {
			current.User = value
		} else if key == "port" {
			if p, err := strconv.Atoi(value); err == nil {
				current.Port = p
			}
		} else if key == "identityfile" {
			current.KeyPath = value
			current.AuthType = "key"
		}
	}
	// Don't forget the last one.
	if current != nil && current.Host != "" {
		configs = append(configs, *current)
	}
	return configs
}

// Ensure imports are used.
var _ = net.Dial
