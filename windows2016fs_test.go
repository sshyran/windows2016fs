package windows2016fs_test

import (
	"encoding/json"
	"fmt"
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

var (
	SESSION_TIMEOUT = 10 * time.Minute
)

func expectCommand(executable string, params ...string) {
	command := exec.Command(executable, params...)
	session, err := Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())
	Eventually(session, SESSION_TIMEOUT).Should(Exit(0))
}

func lookupEnv(envName string) string {
	value, ok := os.LookupEnv(envName)
	if !ok {
		Fail(fmt.Sprintf("Environment variable %s must be set", envName))
	}

	return value
}

func buildDockerImage(tempDirPath, depDir, imageNameAndTag, tag string) {
	dockerSrcPath := filepath.Join(tag, "Dockerfile")
	Expect(dockerSrcPath).To(BeARegularFile())

	Expect(depDir).To(BeADirectory())

	expectCommand("powershell", "Copy-Item", "-Path", dockerSrcPath, "-Destination", tempDirPath)

	expectCommand("powershell", "Copy-Item", "-Path", filepath.Join(depDir, "*"), "-Destination", tempDirPath)

	expectCommand(
		"docker",
		"build",
		"-f", filepath.Join(tempDirPath, "Dockerfile"),
		"--tag", imageNameAndTag,
		"--pull",
		tempDirPath,
	)
}

func buildTestDockerImage(imageNameAndTag, testImageNameAndTag string) {
	expectCommand(
		"docker",
		"build",
		"-f", filepath.Join("fixtures", "test.Dockerfile"),
		"--build-arg", fmt.Sprintf("CI_IMAGE_NAME_AND_TAG=%s", imageNameAndTag),
		"--tag", testImageNameAndTag,
		"fixtures",
	)
}

func expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, imageNameAndTag string) {
	command := exec.Command(
		"docker",
		"run",
		"--rm",
		"--user", "vcap",
		"--env", fmt.Sprintf("SHARE_UNC=%s", shareUnc),
		"--env", fmt.Sprintf("SHARE_USERNAME=%s", shareUsername),
		"--env", fmt.Sprintf("SHARE_PASSWORD=%s", sharePassword),
		imageNameAndTag,
		"powershell",
		`.\container-test.ps1`,
	)

	session, err := Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())

	Eventually(session, SESSION_TIMEOUT).Should(Exit(0))

	smbMapping := string(session.Out.Contents())
	Expect(smbMapping).To(ContainSubstring("T:"))
	Expect(smbMapping).To(ContainSubstring(shareUnc))
}

type serviceState struct {
	Name      string
	StartType int
	Status    int
}

var _ = Describe("Windows2016fs", func() {
	var (
		tag                 string
		imageNameAndTag     string
		testImageNameAndTag string
		tempDirPath         string
		shareUsername       string
		sharePassword       string
		shareName           string
		shareIP             string
		shareFqdn           string
		err                 error
	)

	BeforeSuite(func() {
		tempDirPath, err = ioutil.TempDir("", "build")
		Expect(err).NotTo(HaveOccurred())

		shareName = lookupEnv("SHARE_NAME")
		shareUsername = lookupEnv("SHARE_USERNAME")
		sharePassword = lookupEnv("SHARE_PASSWORD")
		shareFqdn = lookupEnv("SHARE_FQDN")
		shareIP = lookupEnv("SHARE_IP")
		tag = lookupEnv("VERSION_TAG")
		testImageNameAndTag = fmt.Sprintf("windows2016fs-test:%s", tag)

		if os.Getenv("TEST_CANDIDATE_IMAGE") == "" {
			depDir := lookupEnv("DEPENDENCIES_DIR")
			imageNameAndTag = fmt.Sprintf("windows2016fs-candidate:%s", tag)
			buildDockerImage(tempDirPath, depDir, imageNameAndTag, tag)
		} else {
			imageNameAndTag = os.Getenv("TEST_CANDIDATE_IMAGE")
		}
	})

	It("can write to an IP-based smb share", func() {
		shareUnc := fmt.Sprintf(`\\%s\%s`, shareIP, shareName)
		buildTestDockerImage(imageNameAndTag, testImageNameAndTag)

		expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, testImageNameAndTag)
	})

	It("can write to an FQDN-based smb share", func() {
		shareUnc := fmt.Sprintf(`\\%s\%s`, shareFqdn, shareName)
		buildTestDockerImage(imageNameAndTag, testImageNameAndTag)
		expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, testImageNameAndTag)
	})

	It("can access one share multiple times on the same VM", func() {
		shareUnc := fmt.Sprintf(`\\%s\%s`, shareIP, shareName)
		buildTestDockerImage(imageNameAndTag, testImageNameAndTag)

		concurrentConnections := 10
		wg := new(sync.WaitGroup)
		wg.Add(concurrentConnections)

		for i := 1; i <= concurrentConnections; i++ {
			go func() {
				expectMountSMBImage(shareUnc, shareUsername, sharePassword, tempDirPath, testImageNameAndTag)
				wg.Done()
			}()
		}

		wg.Wait()
	})

	It("has expected list of services", func() {
		Skip("this test is brittle and serves little value")

		//Expected baseline service generated by: `docker run cloudfoundry/windows2016fs:2019 powershell "Get-Service | ConvertTo-JSON" > .\fixtures\expected-baseline-services-2019.json`
		jsonData, err := ioutil.ReadFile(filepath.Join("fixtures", fmt.Sprintf("expected-baseline-services-%s.json", tag)))
		Expect(err).ToNot(HaveOccurred())

		var baselineServices []serviceState
		err = json.Unmarshal(jsonData, &baselineServices)
		Expect(err).ToNot(HaveOccurred())

		command := exec.Command(
			"docker",
			"run",
			"--rm",
			imageNameAndTag,
			"powershell", "Get-Service | ConvertTo-JSON",
		)

		session, err := Start(command, GinkgoWriter, GinkgoWriter)
		Expect(err).ToNot(HaveOccurred())
		Eventually(session, SESSION_TIMEOUT).Should(Exit(0))

		actualServicesPowershellJSON := session.Out.Contents()

		var actualServices []serviceState
		err = json.Unmarshal(actualServicesPowershellJSON, &actualServices)
		Expect(err).ToNot(HaveOccurred())

		Expect(actualServices).To(Equal(baselineServices))
	})

	It("has expected version of .NET Framework", func() {
		command := exec.Command(
			"docker",
			"run",
			"--rm",
			imageNameAndTag,
			"powershell", `Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\NET Framework Setup\NDP\v4\Full\' | Get-ItemPropertyValue -Name Release`,
		)

		session, err := Start(command, GinkgoWriter, GinkgoWriter)
		Expect(err).ToNot(HaveOccurred())
		Eventually(session, SESSION_TIMEOUT).Should(Exit(0))

		actualFrameworkRelease := strings.TrimSpace(string(session.Out.Contents()))

		var expectedFrameworkRelease string

		// https://docs.microsoft.com/en-us/dotnet/framework/migration-guide/release-keys-and-os-versions
		if tag == "2019" {
			expectedFrameworkRelease = "528049" //Framework version 4.8
		} else {
			Fail(fmt.Sprintf("unknown tag: %+s", tag))
		}

		Expect(actualFrameworkRelease).To(Equal(expectedFrameworkRelease))
	})

	It("can import a registry file", func() {
		buildTestDockerImage(imageNameAndTag, testImageNameAndTag)

		command := exec.Command(
			"docker",
			"run",
			"--rm",
			"--user", "vcap",
			testImageNameAndTag,
			"cmd", "/c",
			`reg import odbc.reg`,
		)

		_, err := command.StdinPipe()
		Expect(err).ToNot(HaveOccurred())

		session, err := Start(command, GinkgoWriter, GinkgoWriter)
		Expect(err).ToNot(HaveOccurred())

		Eventually(session, SESSION_TIMEOUT).Should(Exit(0))

		Expect(string(session.Err.Contents())).To(ContainSubstring("The operation completed successfully."))
	})

	It("contains Visual C++ restributable for 2010", func() {
		buildTestDockerImage(imageNameAndTag, testImageNameAndTag)

		expectCommand(
			"docker",
			"run",
			"--rm",
			testImageNameAndTag,
			"powershell", `Get-ChildItem C:\Windows\System32\msvcr100.dll`,
		)
	})

	It("contains Visual C++ restributable for 2015+", func() {
		buildTestDockerImage(imageNameAndTag, testImageNameAndTag)

		expectCommand(
			"docker",
			"run",
			"--rm",
			testImageNameAndTag,
			"powershell", `Get-ChildItem C:\Windows\System32\vcruntime140.dll`,
		)
	})
})
