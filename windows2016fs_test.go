package windows2016fs_test

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gexec"
)

func expectCommand(executable string, params ...string) {
	command := exec.Command(executable, params...)
	session, err := Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())
	Eventually(session, 10*time.Minute).Should(Exit(0))
}

func lookupEnv(envName string) string {
	value, ok := os.LookupEnv(envName)
	if !ok {
		Fail(fmt.Sprintf("Environment variable %s must be set", envName))
	}

	return value
}

func buildDockerImage(tempDirPath, depDir, imageId, tag string) {
	dockerSrcPath := filepath.Join(tag, "Dockerfile")
	Expect(dockerSrcPath).To(BeARegularFile())

	Expect(depDir).To(BeADirectory())

	expectCommand("powershell", "Copy-Item", "-Path", dockerSrcPath, "-Destination", tempDirPath)

	expectCommand("powershell", "Copy-Item", "-Path", filepath.Join(depDir, "*"), "-Destination", tempDirPath)

	expectCommand("docker", "build", "-f", filepath.Join(tempDirPath, "Dockerfile"), "--tag", imageId, tempDirPath)
}

func expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, imageId string) {
	command := exec.Command(
		"docker",
		"run",
		"--rm",
		"--interactive",
		"--env", fmt.Sprintf("SHARE_UNC=%s", shareUnc),
		"--env", fmt.Sprintf("SHARE_USERNAME=%s", shareUsername),
		"--env", fmt.Sprintf("SHARE_PASSWORD=%s", sharePassword),
		imageId,
		"powershell",
	)

	stdin, err := command.StdinPipe()
	Expect(err).ToNot(HaveOccurred())

	session, err := Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())

	containerTestPs1Content, err := ioutil.ReadFile("container-test.ps1")
	Expect(err).ToNot(HaveOccurred())

	_, err = io.WriteString(stdin, string(containerTestPs1Content))
	Expect(err).ToNot(HaveOccurred())
	stdin.Close()

	Expect(err).ToNot(HaveOccurred())
	Eventually(session, 5*time.Minute).Should(Exit(0))
}

type serviceState struct {
	Name      string
	StartType int
}

func diff(left []serviceState, right []serviceState) map[string][]serviceState {
	var serviceDiffs = make(map[string][]serviceState)

	//set baseline states in [0] position
	for _, service := range left {
		serviceDiffs[strings.ToLower(service.Name)] = []serviceState{
			service,
			{},
		}
	}

	//set actual states in [1] position
	for _, rightServiceState := range right {
		diff, ok := serviceDiffs[strings.ToLower(rightServiceState.Name)]

		if !ok {
			serviceDiffs[strings.ToLower(rightServiceState.Name)] = []serviceState{
				{},
				rightServiceState,
			}
		} else {
			diff[1] = rightServiceState
		}
	}

	//remove identical states
	for serviceName, diff := range serviceDiffs {
		if diff[0] == diff[1] {
			delete(serviceDiffs, serviceName)
		}
	}

	return serviceDiffs
}

var _ = Describe("Windows2016fs", func() {
	var (
		tag            string
		imageId        string
		tempDirPath    string
		shareUsername  string
		shareUsername2 string
		sharePassword  string
		shareName      string
		shareIP        string
		shareFqdn      string
		err            error
	)

	BeforeSuite(func() {
		tempDirPath, err = ioutil.TempDir("", "build")
		Expect(err).NotTo(HaveOccurred())

		shareName = lookupEnv("SHARE_NAME")
		shareUsername = lookupEnv("SHARE_USERNAME")
		shareUsername2 = lookupEnv("SHARE_USERNAME2")
		sharePassword = lookupEnv("SHARE_PASSWORD")
		shareFqdn = lookupEnv("SHARE_FQDN")
		shareIP = lookupEnv("SHARE_IP")
		tag = lookupEnv("VERSION_TAG")
		imageId = fmt.Sprintf("windows2016fs-ci:%s", tag)
		depDir := lookupEnv("DEPENDENCIES_DIR")

		buildDockerImage(tempDirPath, depDir, imageId, tag)
	})

	It("can write to an IP-based smb share", func() {
		shareUnc := fmt.Sprintf("\\\\%s\\%s", shareIP, shareName)
		expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, imageId)
	})

	It("can write to an FQDN-based smb share", func() {
		shareUnc := fmt.Sprintf("\\\\%s\\%s", shareFqdn, shareName)
		expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, imageId)
	})

	It("can access one share multiple times, with multiple different credentials on the same VM", func() {
		shareUnc := fmt.Sprintf("\\\\%s\\%s", shareIP, shareName)

		wg := new(sync.WaitGroup)
		wg.Add(2)

		go func() {
			expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, imageId)
			wg.Done()
		}()

		go func() {
			expectMountSMBImage(shareUnc, shareUsername2, sharePassword, tempDirPath, imageId)
			wg.Done()
		}()
		wg.Wait()
	})

	It("has expected list of services", func() {
		var err error

		jsonData, err := ioutil.ReadFile(filepath.Join("fixtures", "expected-baseline-services.json"))
		Expect(err).ToNot(HaveOccurred())

		var baselineServices []serviceState
		err = json.Unmarshal(jsonData, &baselineServices)
		Expect(err).ToNot(HaveOccurred())

		command := exec.Command(
			"docker",
			"run",
			"--rm",
			imageId,
			"powershell", "Get-Service | ConvertTo-JSON",
		)

		session, err := Start(command, GinkgoWriter, GinkgoWriter)
		Expect(err).ToNot(HaveOccurred())
		Eventually(session, 30*time.Second).Should(Exit(0))

		actualServicesPowershellJSON := session.Out.Contents()

		var actualServices []serviceState
		err = json.Unmarshal(actualServicesPowershellJSON, &actualServices)
		Expect(err).ToNot(HaveOccurred())

		var expectedDiffFromBaseline map[string][]serviceState

		switch tag {
		case "1709":
			expectedDiffFromBaseline = map[string][]serviceState{}
		case "1803":
			expectedDiffFromBaseline = map[string][]serviceState{
				"mpssvc": {
					{Name: "MpsSvc", StartType: 4},
					{Name: "mpssvc", StartType: 4},
				},
				"usosvc": {
					{Name: "UsoSvc", StartType: 3},
					{Name: "UsoSvc", StartType: 2},
				},
				"wdnissvc": {
					{Name: "WdNisSvc", StartType: 3},
					{},
				},
				"windefend": {
					{Name: "WinDefend", StartType: 4},
					{},
				},
				"ssh-agent": {
					{},
					{Name: "ssh-agent", StartType: 4},
				},
				"scardsvr": {
					{Name: "SCardSvr", StartType: 4},
					{Name: "SCardSvr", StartType: 3},
				},
				"sense": {
					{},
					{Name: "Sense", StartType: 3},
				},
			}
		case "2019":
			expectedDiffFromBaseline = map[string][]serviceState{
				"mpssvc": {
					{Name: "MpsSvc", StartType: 4},
					{Name: "mpssvc", StartType: 4},
				},
				"clipsvc": {
					{Name: "ClipSVC", StartType: 3},
					{Name: "ClipSVC", StartType: 4},
				},
				"fdphost": {
					{Name: "fdPHost", StartType: 3},
					{},
				},
				"fontcache": {
					{Name: "FontCache", StartType: 4},
					{},
				},
				"scardsvr": {
					{Name: "SCardSvr", StartType: 4},
					{Name: "SCardSvr", StartType: 3},
				},
				"spooler": {
					{Name: "Spooler", StartType: 4},
					{},
				},
				"sppsvc": {
					{Name: "sppsvc", StartType: 2},
					{Name: "sppsvc", StartType: 4},
				},
				"sysmain": {
					{Name: "SysMain", StartType: 3},
					{Name: "SysMain", StartType: 2},
				},
				"usosvc": {
					{Name: "UsoSvc", StartType: 3},
					{Name: "UsoSvc", StartType: 2},
				},
				"wdnissvc": {
					{Name: "WdNisSvc", StartType: 3},
					{},
				},
				"windefend": {
					{Name: "WinDefend", StartType: 4},
					{},
				},
				"appreadiness": {
					{},
					{Name: "AppReadiness", StartType: 3},
				},
				"sense": {
					{},
					{Name: "Sense", StartType: 3},
				},
				"sgrmbroker": {
					{},
					{Name: "SgrmBroker", StartType: 3},
				},
				"ssh-agent": {
					{},
					{Name: "ssh-agent", StartType: 4},
				},
				"waasmedicsvc": {
					{},
					{Name: "WaaSMedicSvc", StartType: 4},
				},
			}
		default:
			Fail(fmt.Sprintf("unknown tag: %+s", tag))
		}

		actualDiffFromBaseline := diff(baselineServices, actualServices)

		Expect(actualDiffFromBaseline).To(Equal(expectedDiffFromBaseline))
	})

	It("has expected version of .NET Framework", func() {
		var err error

		command := exec.Command(
			"docker",
			"run",
			"--rm",
			imageId,
			"powershell", `Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\NET Framework Setup\NDP\v4\Full\' | Get-ItemPropertyValue -Name Release`,
		)

		session, err := Start(command, GinkgoWriter, GinkgoWriter)
		Expect(err).ToNot(HaveOccurred())
		Eventually(session, 30*time.Second).Should(Exit(0))

		actualFrameworkRelease := strings.TrimSpace(string(session.Out.Contents()))

		var expectedFrameworkRelease string

		// https://docs.microsoft.com/en-us/dotnet/framework/migration-guide/release-keys-and-os-versions
		switch tag {
		case "1709":
			expectedFrameworkRelease = "461308" //Framwork version 4.7.1 (link: "...Windows Server, version 1709")
		case "1803":
			expectedFrameworkRelease = "461808" //Framwork version 4.7.2 (link: "...Windows Server, version 1803")
		case "2019":
			expectedFrameworkRelease = "461814" //Framwork version 4.7.2 (link: "1803...all other Windows operating systems")
		default:
			Fail(fmt.Sprintf("unknown tag: %+s", tag))
		}

		Expect(actualFrameworkRelease).To(Equal(expectedFrameworkRelease))
	})
})
