package minivmm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/pkg/errors"
	"github.com/yaamai/govmm/qemu"
)

var (
	qmpSocketFileName         = "qmp.socket"
	vncSocketFileName         = "vnc.socket"
	vmMetaDataFileName        = "metadata.json"
	cloudInitISOFileName      = "cloud-init.iso"
	cloudInitUserDataFileName = "user-data"
	cloudInitMetaDataFileName = "meta-data"
	// VMIPAddressUpdateChan is a channel to update IP address by DHCP server
	VMIPAddressUpdateChan = make(chan *VMMetaData)
)

var vmIFSetupScriptTemplate = `#!/bin/sh
if_name=$1
sudo ip link set dev $if_name netns minivmm
sudo ip netns exec minivmm ip link set dev $if_name master br-minivmm
sudo ip netns exec minivmm ip link set dev $if_name promisc on
sudo ip netns exec minivmm ip link set dev $if_name up
`

var vmIFCleanupScriptTemplate = `#!/bin/sh
if_name=$1
sudo ip netns exec minivmm ip link set dev $if_name down
sudo ip netns exec minivmm ip link set dev $if_name promisc off
sudo ip netns exec minivmm ip link set dev $if_name nomaster
sudo ip netns exec minivmm ip link set dev $if_name netns 1
`

// VMMetaData is VM's metadata.
type VMMetaData struct {
	Name         string        `json:"name"`
	Status       string        `json:"status"`
	Owner        string        `json:"owner"`
	Image        string        `json:"image"`
	Arch         string        `json:"arch"`
	Volume       string        `json:"volume"`
	MacAddress   string        `json:"mac_address"`
	IPAddress    string        `json:"ip_address"`
	CPU          string        `json:"cpu"`
	Memory       string        `json:"memory"`
	Disk         string        `json:"disk"`
	Tag          string        `json:"tag"`
	Lock         bool          `json:"lock"`
	VNCPassword  string        `json:"vnc_password"`
	VNCPort      string        `json:"vnc_port"`
	UserData     string        `json:"user_data"`
	CloudInitIso string        `json:"cloud_init_iso"`
	ExtraVolumes []ExtraVolume `json:"extra_volumes"`
}

// ExtraVolume is extra volume's metadata
type ExtraVolume struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size string `json:"size"`
}

func exists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

func prepareVMIF(ifName string) error {
	return Execs([][]string{
		{"sudo", "ip", "tuntap", "add", "dev", ifName, "mode", "tap"},
	})
}

func cleanupVMIF(ifName string) error {
	return Execs([][]string{
		{"sudo", "ip", "link", "del", "dev", ifName},
	})
}

func isExistsVMIF(ifName string) bool {
	err := Execs([][]string{
		{"sudo", "ip", "netns", "exec", "minivmm", "ip", "link", "show", "dev", ifName},
	})
	if err == nil {
		return true
	}

	err = Execs([][]string{
		{"sudo", "ip", "link", "show", "dev", ifName},
	})
	if err == nil {
		return true
	}

	// FIXME: extend Execs and check return-code instead of error message check
	return !strings.Contains(err.Error(), "does not exist.")
}

func getMachineArch() (string, error) {
	u := syscall.Utsname{}
	err := syscall.Uname(&u)
	if err != nil {
		return "", err
	}

	var m string
	for _, val := range u.Machine {
		if int(val) == 0 {
			continue
		}
		m += string(int(val))
	}

	return m, nil
}

func generateQemuParams(qmpSocketPath, vncSocketPath, driveFilePath, machineArch, cloudInitISOPath, vmMACAddr, vmIFName, cpu, memory string, extraVolumes []string) []string {
	params := make([]string, 0, 32)

	if !C.NoKvm {
		params = append(params, "--enable-kvm")
		params = append(params, "-cpu", "host")
	}

	envVNCKeyboardLayout := C.VNCKeyboardLayout

	params = append(params, "-drive", fmt.Sprintf("file=%s,if=virtio,cache=none,aio=threads,format=qcow2", driveFilePath))
	if extraVolumes != nil {
		for _, vol := range extraVolumes {
			params = append(params, "-drive", fmt.Sprintf("file=%s,if=virtio,cache=none,aio=threads,format=qcow2", vol))
		}
	}

	if machineArch == "aarch64" {
		params = append(params, "-machine", "virt")
		params = append(params, "-bios", "/usr/share/qemu-efi-aarch64/QEMU_EFI.fd")
	}

	params = append(params, "-cdrom", cloudInitISOPath)
	params = append(params, "-net", fmt.Sprintf("nic,model=virtio,macaddr=%s", vmMACAddr))
	params = append(params, "-net", fmt.Sprintf("tap,ifname=%s,script=/tmp/ifup,downscript=/tmp/ifdown", vmIFName))
	params = append(params, "-daemonize")
	params = append(params, "-qmp", fmt.Sprintf("unix:%s,server,nowait", qmpSocketPath))
	params = append(params, "-m", memory, "-smp", fmt.Sprintf("cpus=%s", cpu))
	params = append(params, "-vnc", fmt.Sprintf("unix:%s", vncSocketPath))
	params = append(params, "-k", envVNCKeyboardLayout)

	return params
}

func generateMACAddress() string {
	vendor := "52:54:00"
	buf := make([]byte, 3)
	rand.Read(buf)
	return fmt.Sprintf("%s:%02x:%02x:%02x", vendor, buf[0], buf[1], buf[2])
}

func generateVMIFSetupScript(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer f.Close()
	t := template.Must(template.New("ifscript").Parse(vmIFSetupScriptTemplate))
	err = t.Execute(f, nil)
	if err != nil {
		return err
	}
	return nil
}

func generateVMIFCleanupScript(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer f.Close()
	t := template.Must(template.New("ifscript").Parse(vmIFCleanupScriptTemplate))
	err = t.Execute(f, nil)
	if err != nil {
		return err
	}
	return nil
}

func initQMP(qmpSocketPath string) (*qemu.QMP, chan struct{}, error) {
	disconnectedCh := make(chan struct{})
	cfg := qemu.QMPConfig{}
	q, _, err := qemu.QMPStart(context.Background(), qmpSocketPath, cfg, disconnectedCh)
	if err != nil {
		return nil, nil, err
	}
	// must call capabilities check cmd (if missing, following method will fail)
	err = q.ExecuteQMPCapabilities(context.Background())
	if err != nil {
		return nil, nil, err
	}

	return q, disconnectedCh, nil
}

func getQMPSocketPath(name string) string {
	return filepath.Join(C.VMDir, name, qmpSocketFileName)
}

func getVNCSocketPath(name string) string {
	return filepath.Join(C.VMDir, name, vncSocketFileName)
}

func generateRandomPassword() (string, error) {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(b), err
}

// GetVncPort returns VNC port number of the specified VM.
func GetVncPort(name string) (string, error) {
	q, _, err := initQMP(getQMPSocketPath(name))
	if err != nil {
		return "", errors.Wrap(err, "QMP connection failed")
	}
	defer q.Shutdown()

	r, err := q.ExecuteRawCommand(context.Background(), "query-vnc", map[string]interface{}{}, nil)
	if err != nil {
		return "", errors.Wrap(err, "query-vnc command failed")
	}
	m, ok := r.(map[string]interface{})
	if !ok {
		return "", errors.New("could not parse query-vnc command response")
	}
	port, ok := m["service"].(string)
	if !ok {
		return "", errors.New("could not parse query-vnc command response")
	}

	return port, nil
}

func saveVMMetaData(name string, metaData *VMMetaData) error {
	metaDataByte, err := json.Marshal(metaData)
	if err != nil {
		return err
	}

	vmDataDir := filepath.Join(C.VMDir, name)
	os.MkdirAll(vmDataDir, os.ModePerm)
	metaDataPath := filepath.Join(vmDataDir, vmMetaDataFileName)
	f, err := os.OpenFile(metaDataPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	lockpath := filepath.Join(vmDataDir, vmMetaDataFileName+".lock")
	err = WriteWithLock(f, lockpath, metaDataByte)
	if err != nil {
		return err
	}

	return nil
}

func loadVMMetaData(name string) (*VMMetaData, error) {
	metaDataPath := filepath.Join(C.VMDir, name, vmMetaDataFileName)
	vmMetaData := VMMetaData{}

	metaDataByte, err := ioutil.ReadFile(metaDataPath)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(metaDataByte, &vmMetaData)
	return &vmMetaData, nil
}

func createCloudInitISO(cloudInitFilesPath, isoPath, name, userData string) error {
	// write userdata
	userDataPath := filepath.Join(cloudInitFilesPath, cloudInitUserDataFileName)
	userDataFile, err := os.OpenFile(userDataPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer userDataFile.Close()
	userDataFile.Write([]byte(userData))

	// write metadata
	// maybe cloud-init require meta-data file exists
	metaDataPath := filepath.Join(cloudInitFilesPath, cloudInitMetaDataFileName)
	metaDataFile, err := os.OpenFile(metaDataPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer metaDataFile.Close()
	metaData := fmt.Sprintf("local-hostname: %s", name)
	metaDataFile.Write([]byte(metaData))

	err = Execs([][]string{
		{"genisoimage", "-output", isoPath, "-volid", "cidata", "-joliet", "-rock", userDataPath, metaDataPath},
	})
	return err
}

// CreateVM creates new VM and starts it.
func CreateVM(name, owner, imageName, cpu, memory, disk, userData, tag string) (ret *VMMetaData, retErr error) {
	if exists(filepath.Join(C.VMDir, name, vmMetaDataFileName)) {
		return nil, errors.Errorf("CreateVM: VM '%s' already exists", name)
	}

	defer func() {
		if retErr != nil && name != "" {
			rmErr := os.RemoveAll(filepath.Join(C.VMDir, name))
			if rmErr != nil {
				log.Println("Ignore RemoveAll error:", rmErr)
			}
		}
	}()

	vmDataDir := filepath.Join(C.VMDir, name)
	driveFilePath, err := CreateImage(name, disk, imageName, vmDataDir)
	if err != nil {
		return nil, err
	}

	// to support cloud-init, generate userdata ISO
	isoFilePath := filepath.Join(C.VMDir, name, cloudInitISOFileName)
	userDataPath := filepath.Join(C.VMDir, name)
	err = createCloudInitISO(userDataPath, isoFilePath, name, userData)
	if err != nil {
		return nil, err
	}

	vmMACAddr := generateMACAddress()
	password, _ := generateRandomPassword()

	machineArch, err := getMachineArch()
	if err != nil {
		log.Println(err)
		machineArch = "x86_64"
	}

	metaData := &VMMetaData{
		Name:         name,
		Owner:        owner,
		Image:        imageName,
		Arch:         machineArch,
		Volume:       driveFilePath,
		MacAddress:   vmMACAddr,
		CPU:          cpu,
		Memory:       memory,
		Disk:         disk,
		Tag:          tag,
		Lock:         false,
		VNCPassword:  password,
		VNCPort:      "",
		UserData:     userData,
		CloudInitIso: isoFilePath,
	}
	err = saveVMMetaData(name, metaData)
	if err != nil {
		return nil, err
	}

	metaData, err = StartVM(name)
	if err != nil {
		return nil, err
	}
	// Save metadata again because some parameters will be updated when VM starts
	err = saveVMMetaData(name, metaData)
	if err != nil {
		return nil, err
	}

	return metaData, nil
}

// StopVM shuts down VM.
func StopVM(name string) error {
	status := getVMStatus(name)
	if status == "stopped" {
		// VM has already stopped
		return nil
	}

	q, disconnectedCh, err := initQMP(getQMPSocketPath(name))
	if err != nil {
		return errors.Wrap(err, "StopVM: QMP connection cannot established")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = q.ExecuteSystemPowerdown(ctx)
	cancel()
	if err != nil {
		err = q.ExecuteQuit(context.Background())
		if err != nil {
			return errors.Wrap(err, "StopVM: ExecuteQuit failed")
		}
	}

	q.Shutdown()
	<-disconnectedCh

	return nil
}

func prepareStartVM(name string, metaData *VMMetaData) ([]string, error) {
	qmpSocketPath := getQMPSocketPath(name)
	vncSocketPath := getVNCSocketPath(name)
	driveFilePath := metaData.Volume
	machineArch := metaData.Arch
	cloudInitISOPath := metaData.CloudInitIso
	vmMACAddr := metaData.MacAddress
	cpu := metaData.CPU
	memory, err := convertSIPrefixedValue(metaData.Memory, "mebi")
	if err != nil {
		return nil, err
	}
	extraVolumes := []string{}
	if metaData.ExtraVolumes != nil {
		for _, vol := range metaData.ExtraVolumes {
			extraVolumes = append(extraVolumes, vol.Path)
		}
	}
	vmIFName := fmt.Sprintf("tap-%s", name)
	prepareVMIF(vmIFName)
	qemuParams := generateQemuParams(qmpSocketPath, vncSocketPath, driveFilePath, machineArch, cloudInitISOPath, vmMACAddr, vmIFName, cpu, memory, extraVolumes)

	log.Println("Prepare if script ...")
	err = generateVMIFSetupScript("/tmp/ifup")
	if err != nil {
		return nil, errors.Wrap(err, "StartVM: VM interface setup script generate failed")
	}
	err = generateVMIFCleanupScript("/tmp/ifdown")
	if err != nil {
		return nil, errors.Wrap(err, "StartVM: VM interface setup script generate failed")
	}

	log.Println("Launching vm with: ", driveFilePath, qmpSocketFileName, qemuParams)
	return qemuParams, nil
}

// StartVM starts VM.
func StartVM(name string) (*VMMetaData, error) {
	metaData, err := loadVMMetaData(name)
	if err != nil {
		return nil, errors.Wrap(err, "StartVM: VM metadata load failed")
	}

	status := getVMStatus(name)
	if status != "stopped" {
		return nil, errors.New("Cannot start non-stopped VM")
	}

	qemuBinaryName := "qemu-system-" + metaData.Arch
	qemuParams, err := prepareStartVM(name, metaData)
	stdErr, err := qemu.LaunchCustomQemu(context.Background(), qemuBinaryName, qemuParams, nil, nil, nil)
	if err != nil {
		log.Println(stdErr)
		return nil, errors.Wrap(err, "StartVM: VM launch failed")
	}

	port, err := GetVncPort(name)
	if err != nil {
		return nil, err
	}
	metaData.VNCPort = port

	return metaData, nil
}

// ResizeVM updates the size metadata of VM.
func ResizeVM(name, cpu, memory, disk string) (*VMMetaData, error) {
	metaData, err := GetVM(name)
	if err != nil {
		return nil, errors.Wrap(err, "ResizeVM: Failed to get VM metadata")
	}

	if cpu != "" {
		metaData.CPU = cpu
	}
	if memory != "" {
		metaData.Memory = memory
	}
	if disk != "" {
		metaData.Disk = disk
	}
	err = saveVMMetaData(name, metaData)
	if err != nil {
		return nil, err
	}

	return metaData, nil
}

// LockVM lock the VM to prevent from some operations.
func LockVM(name string) (*VMMetaData, error) {
	return setVMLock(name, true)
}

// UnlockVM unlock the VM.
func UnlockVM(name string) (*VMMetaData, error) {
	return setVMLock(name, false)
}

func setVMLock(name string, lock bool) (*VMMetaData, error) {
	metaData, err := GetVM(name)
	if err != nil {
		return nil, errors.Wrap(err, "setVMLock: Failed to get VM metadata")
	}

	metaData.Lock = lock

	err = saveVMMetaData(name, metaData)
	if err != nil {
		return nil, err
	}

	return metaData, nil
}

// AddVolume adds a new extra volume to the VM
func AddVolume(name, size string) (*VMMetaData, error) {
	metaData, err := GetVM(name)
	if err != nil {
		return nil, errors.Wrap(err, "AddVolume: Failed to get VM metadata")
	}

loop:
	for i := 1; i <= 256; i++ {
		imageName := fmt.Sprintf("extra-volume%d", i)
		for _, vol := range metaData.ExtraVolumes {
			if imageName == vol.Name {
				continue loop
			}
		}

		vmDataDir := filepath.Join(C.VMDir, name)
		path, err := CreateImage(imageName, size, "", vmDataDir)
		if err != nil {
			return nil, errors.Wrap(err, "AddVolume: Failed to create image")
		}

		ev := ExtraVolume{Name: imageName, Path: path, Size: size}
		metaData.ExtraVolumes = append(metaData.ExtraVolumes, ev)

		err = saveVMMetaData(name, metaData)
		if err != nil {
			os.Remove(path)
			return nil, err
		}

		return metaData, nil
	}

	return nil, errors.New("The maximum number of extra volumes is 256")
}

// RemoveVolume removes a extra volume from the VM
func RemoveVolume(name, volName string) (*VMMetaData, error) {
	metaData, err := GetVM(name)
	if err != nil {
		return nil, errors.Wrap(err, "RemoveVolume: Failed to get VM metadata")
	}
	if metaData.Lock {
		return nil, errors.New("VM is locked")
	}

	for i, vol := range metaData.ExtraVolumes {
		if volName == vol.Name {
			os.Remove(vol.Path)

			metaData.ExtraVolumes = append(metaData.ExtraVolumes[:i], metaData.ExtraVolumes[i+1:]...)

			err = saveVMMetaData(name, metaData)
			if err != nil {
				return nil, err
			}

			return metaData, nil
		}
	}

	return nil, fmt.Errorf("Cannot remove '%s'. No such a image file", volName)
}

func getVMStatus(name string) string {
	// VM status not saved in metadata
	q, _, err := initQMP(getQMPSocketPath(name))
	if err != nil {
		return "stopped"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	statusInfo, err := q.ExecuteQueryStatus(ctx)
	cancel()
	if err != nil {
		log.Println("getVMStatus: ", err)
		return "unknown"
	}
	q.Shutdown()

	return statusInfo.Status
}

// GetVM returns VM metadata.
func GetVM(name string) (*VMMetaData, error) {
	metaData, err := loadVMMetaData(name)
	if err != nil {
		return nil, err
	}

	status := getVMStatus(name)
	metaData.Status = status

	return metaData, nil
}

// GetVMFromMac returns VM metadata.
func GetVMFromMac(mac string) (*VMMetaData, error) {
	metaDataList, err := ListVMs()
	if err != nil {
		return nil, err
	}

	for _, metaData := range metaDataList {
		if metaData.MacAddress == mac {
			return metaData, nil
		}
	}

	return nil, fmt.Errorf("GetVMFromMac: Cannot find vm with '%s'", mac)
}

// ListVMs returns a list of VM metadata.
func ListVMs() ([]*VMMetaData, error) {
	dirEntries, err := ioutil.ReadDir(C.VMDir)
	if err != nil {
		return nil, errors.Wrap(err, "ListVMs: Cannot read vm data dir")
	}

	var ret []*VMMetaData
	for _, f := range dirEntries {
		if f.IsDir() {
			m, err := GetVM(f.Name())
			if err != nil {
				log.Println("Ignore GetVM error:", err)
				continue
			}
			ret = append(ret, m)
		}
	}

	return ret, nil
}

// UpdateIPAddress updates IP address in VM metadata.
func UpdateIPAddress() {
	for {
		r := <-VMIPAddressUpdateChan
		e, err := GetVMFromMac(r.MacAddress)
		if err != nil {
			log.Println("Ignore GetVMFromMac error:", err)
			continue
		}

		e.IPAddress = r.IPAddress
		err = saveVMMetaData(e.Name, e)
		if err != nil {
			log.Println("Ignore saveVMMetaData error:", err)
			continue
		}

		UpdateIPAddressInForwarder(e.Name, r.IPAddress)
	}
}

// RemoveVM remove VM
func RemoveVM(name string) error {
	metaData, err := GetVM(name)
	if err != nil {
		return errors.Wrap(err, "RemoveVM: Failed to get VM metadata")
	}
	if metaData.Lock {
		return errors.New("VM is locked")
	}

	err = StopVM(name)
	if err != nil {
		return err
	}

	vmIFName := fmt.Sprintf("tap-%s", name)
	if isExistsVMIF(vmIFName) {
		retryCount := 0
		for {
			if retryCount > 30 {
				return errors.New("VM tap deletion timed out")
			}
			err = cleanupVMIF(vmIFName)
			if err == nil {
				break
			}
			time.Sleep(3 * time.Second)
			retryCount = retryCount + 1
		}
	}

	vmDataDir := filepath.Join(C.VMDir, name)
	err = os.RemoveAll(vmDataDir)
	return err
}
