package runner

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sync"
	"time"

	retryGo "github.com/avast/retry-go/v4"
	log "github.com/sirupsen/logrus"

	"github.com/trento-project/runner/api"
	"github.com/trento-project/runner/internal"
)

//go:embed ansible
var ansibleFS embed.FS

const (
	AnsibleMain            = "ansible/check.yml"
	AnsibleMeta            = "ansible/meta.yml"
	AnsibleConfigFile      = "ansible/ansible.cfg"
	AnsibleHostFile        = "ansible/ansible_hosts"
	CatalogDestinationFile = "ansible/catalog.json"
)

//go:generate mockery --name=RunnerService

type RunnerService interface {
	Start(ctx context.Context) error
	IsCatalogReady() bool
	BuildCatalog() error
}

type runnerService struct {
	config    *Config
	trentoApi api.TrentoApiService
	ready     bool
}

func NewRunnerService(config *Config) (*runnerService, error) {
	runner := &runnerService{
		config: config,
		ready:  false,
	}

	return runner, nil
}

func (c *runnerService) Start(ctx context.Context) error {
	var wg sync.WaitGroup

	if err := createAnsibleFiles(c.config.AnsibleFolder); err != nil {
		return err
	}

	var trentoApi api.TrentoApiService
	err := retryGo.Do(
		func() error {
			trentoApi = api.NewTrentoApiService(c.config.ApiHost, c.config.ApiPort)
			if !trentoApi.IsWebServerUp() {
				return fmt.Errorf("Trento server api not available")
			}
			return nil
		},
		retryGo.OnRetry(func(n uint, err error) {
			log.Error(err)
		}),
		retryGo.Delay(2*time.Second),
		retryGo.MaxJitter(3*time.Second),
		retryGo.Attempts(8),
		retryGo.LastErrorOnly(true),
		retryGo.Context(ctx),
	)
	if err != nil {
		return err
	}

	c.trentoApi = trentoApi

	wg.Add(1)
	go func(wg *sync.WaitGroup) {
		log.Println("Starting the runner loop...")
		defer wg.Done()
		c.startCheckRunnerTicker(ctx)
		log.Println("Runner loop stopped.")
	}(&wg)

	wg.Wait()

	return nil
}

func (c *runnerService) IsCatalogReady() bool {
	return c.ready
}

func (c *runnerService) BuildCatalog() error {
	if err := createAnsibleFiles(c.config.AnsibleFolder); err != nil {
		return err
	}

	metaRunner, err := NewAnsibleMetaRunner(c.config)
	if err != nil {
		return err
	}

	if err = metaRunner.RunPlaybook(); err != nil {
		log.Errorf("Error running the catalog meta-playbook")
		return err
	}

	c.ready = true

	return nil
}

func createAnsibleFiles(folder string) error {
	log.Infof("Creating the ansible file structure in %s", folder)
	// Clean the folder if it stores old files
	ansibleFolder := path.Join(folder, "ansible")
	err := os.RemoveAll(ansibleFolder)
	if err != nil {
		log.Error(err)
		return err
	}

	err = os.MkdirAll(ansibleFolder, 0755)
	if err != nil {
		log.Error(err)
		return err
	}

	// Create the ansible file structure from the FS
	err = fs.WalkDir(ansibleFS, "ansible", func(fileName string, dir fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !dir.IsDir() {
			content, err := ansibleFS.ReadFile(fileName)
			if err != nil {
				log.Errorf("Error reading file %s", fileName)
				return err
			}
			f, err := os.Create(path.Join(folder, fileName))
			if err != nil {
				log.Errorf("Error creating file %s", fileName)
				return err
			}
			fmt.Fprintf(f, "%s", content)
		} else {
			os.Mkdir(path.Join(folder, fileName), 0755)
		}
		return nil
	})

	if err != nil {
		log.Errorf("An error ocurred during the ansible file structure creation: %s", err)
		return err
	}

	log.Info("Ansible file structure successfully created")

	return nil
}

func NewAnsibleMetaRunner(config *Config) (*AnsibleRunner, error) {
	playbookPath := path.Join(config.AnsibleFolder, AnsibleMeta)
	ansibleRunner := DefaultAnsibleRunner()

	if err := ansibleRunner.SetPlaybook(playbookPath); err != nil {
		return ansibleRunner, err
	}

	configFile := path.Join(config.AnsibleFolder, AnsibleConfigFile)
	ansibleRunner.SetConfigFile(configFile)
	destination := path.Join(config.AnsibleFolder, CatalogDestinationFile)
	ansibleRunner.SetCatalogDestination(destination)

	return ansibleRunner, nil
}

func NewAnsibleCheckRunner(config *Config) (*AnsibleRunner, error) {
	playbookPath := path.Join(config.AnsibleFolder, AnsibleMain)

	ansibleRunner := DefaultAnsibleRunner()

	if err := ansibleRunner.SetPlaybook(playbookPath); err != nil {
		return ansibleRunner, err
	}

	ansibleRunner.Check = true
	configFile := path.Join(config.AnsibleFolder, AnsibleConfigFile)
	ansibleRunner.SetConfigFile(configFile)
	ansibleRunner.SetTrentoApiData(config.ApiHost, config.ApiPort)

	return ansibleRunner, nil
}

func (c *runnerService) startCheckRunnerTicker(ctx context.Context) {
	checkRunner, err := NewAnsibleCheckRunner(c.config)
	if err != nil {
		return
	}

	metaRunner, err := NewAnsibleMetaRunner(c.config)
	if err != nil {
		return
	}

	tick := func() {
		if err = metaRunner.RunPlaybook(); err != nil {
			log.Errorf("Error running the catalog meta-playbook")
			return
		}

		content, err := NewClusterInventoryContent(c.trentoApi)
		if err != nil {
			log.Errorf("Error creating the ansible inventory content: %s", err)
			return
		}

		inventoryFile := path.Join(c.config.AnsibleFolder, AnsibleHostFile)
		err = CreateInventory(inventoryFile, content)
		if err != nil {
			log.Errorf("Error creating the ansible inventory file")
			return
		}

		if err = checkRunner.SetInventory(inventoryFile); err != nil {
			log.Errorf("Error setting the ansible inventory file")
			return
		}

		checkRunner.RunPlaybook()
	}

	interval := c.config.Interval
	internal.Repeat("runner.ansible_playbook", tick, interval, ctx)
}
