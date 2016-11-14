package marathon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"koding/kites/kloud/stack"
	"koding/kites/kloud/stack/provider"
	"path"
	"strconv"

	marathon "github.com/gambol99/go-marathon"
)

var klientPort = map[string]interface{}{
	"container_port": 56789,
	"host_port":      0,
	"protocol":       "tcp",
}

var healthCheck = map[string]interface{}{
	"command": map[string]interface{}{
		"value": "curl -f -X GET http://$$HOST:$${PORT_56789}/kite",
	},
	"max_consecutive_failures": 3,
	"protocol":                 "COMMAND",
}

// Stack represents a Marathon application.
type Stack struct {
	*provider.BaseStack

	EntrypointBaseURL string
	KlientURL         string

	AppOrGroupName string
	AppCount       int
	Labels         []string
}

var (
	_ provider.Stack = (*Stack)(nil) // public API
	_ stack.Stacker  = (*Stack)(nil) // internal API
)

func newStack(bs *provider.BaseStack) (provider.Stack, error) {
	s := &Stack{
		BaseStack:         bs,
		EntrypointBaseURL: "https://koding-klient.s3.amazonaws.com/entrypoint",
		KlientURL:         stack.Konfig.KlientGzURL(),
	}

	bs.PlanFunc = s.plan

	return s, nil
}

// VerifyCredential checks whether the given credentials
// can be used for deploying an app into Marathon.
func (s *Stack) VerifyCredential(c *stack.Credential) error {
	client, err := marathon.NewClient(*c.Credential.(*Credential).Config())
	if err != nil {
		return err
	}

	_, err = client.Ping()
	return err
}

// BootstrapTemplate implements the provider.Stack interface.
//
// It is a nop for Marathon.
func (s *Stack) BootstrapTemplates(*stack.Credential) (_ []*stack.Template, _ error) {
	return
}

// StacklyTemplate applies the given credentials to user's stack template.
func (s *Stack) ApplyTemplate(_ *stack.Credential) (*stack.Template, error) {
	t := s.Builder.Template

	var resource struct {
		MarathonApp map[string]map[string]interface{} `hcl:"marathon_app"`
	}

	if err := t.DecodeResource(&resource); err != nil {
		return nil, err
	}

	if len(resource.MarathonApp) == 0 {
		return nil, errors.New("applications are empty")
	}

	for name, app := range resource.MarathonApp {
		originalAppID := s.convertInstancesToGroup(name, app)

		if err := s.injectEntrypoint(app, originalAppID); err != nil {
			return nil, err
		}

		s.injectFetchEntrypoints(app, len(s.Labels))
		s.injectHealthChecks(app)

		if err := s.injectMetadata(app, s.Labels); err != nil {
			return nil, err
		}
	}

	t.Resource["marathon_app"] = resource.MarathonApp

	err := t.ShadowVariables("FORBIDDEN", "marathon_basic_auth_user", "marathon_basic_auth_password")
	if err != nil {
		return nil, errors.New("marathon: error shadowing: " + err.Error())
	}

	if err := t.Flush(); err != nil {
		return nil, errors.New("marathon: error flushing template: " + err.Error())
	}

	content, err := t.JsonOutput()
	if err != nil {
		return nil, err
	}

	return &stack.Template{
		Content: content,
	}, nil
}

// convertInstancesToGroup converts instances property to a count one.
//
// Since Marathon does not support instance indexing it's not possible
// to assign unique metadata for each of the instace, thus making such
// stack unusable for Koding. Relevant issue:
//
//   https://github.com/mesosphere/marathon/issues/1242
//
// What we do instead is we convert multiple instances of an application to
// an application group as a workaround.
func (s *Stack) convertInstancesToGroup(name string, app map[string]interface{}) (originalAppID string) {
	instances, ok := app["instances"].(int)
	if ok {
		delete(app, "instances")
	} else {
		instances = 1
	}

	count, ok := app["count"].(int)
	if !ok {
		count = 1
	}

	count *= instances

	app["count"] = count
	s.AppCount = count

	// Each app within group must have unique name.
	appID, ok := app["app_id"].(string)
	if !ok || appID == "" {
		appID = path.Join("/", name)
		app["app_id"] = appID
	}

	s.AppOrGroupName = appID

	if count > 1 {
		s.AppOrGroupName = path.Base(appID)
		app["app_id"] = path.Join(appID, s.AppOrGroupName+"-${count.index + 1}")
	}

	return appID
}

var ErrIncompatibleEntrypoint = errors.New(`marathon: setting "args" argument conflicts with Koding entrypoint injected into each container. Please use "cmd" argument instead.`)

// injectEntrypoint injects an entrypoint, which is responsible for installing
// klient before running container's command.
//
// The entrypoint is injected in a twofold manner:
//
//   - if "cmd" argument is used, it's prefixed with an entrypoint.N.sh script
//   - if container's default command (the one from Dockerfile) is used,
//     the container's entrypoint (by default /bin/sh) is replaced
//     with the Koding one
//
func (s *Stack) injectEntrypoint(app map[string]interface{}, originalAppID string) error {
	if _, ok := app["args"]; ok {
		return ErrIncompatibleEntrypoint
	}

	count, ok := app["count"].(int)
	if !ok {
		count = 1
	}

	if cmd, ok := app["cmd"].(string); ok && cmd != "" {
		app["cmd"] = "/mnt/mesos/sandbox/entrypoint.${count.index + 1}.sh " + cmd

		// BUG(rjeczalik): when "cmd" argument is set, we assume
		// there's going to be only one klient injected, since
		// it's not possible to wrap containers' entrypoints,
		// as Mesos sets fixed entrypoint to /bin/sh for
		// every container.
		if count == 1 {
			s.Labels = []string{originalAppID}
			return nil
		}

		for i := 0; i < count; i++ {
			s.Labels = append(s.Labels, fmt.Sprintf("%s-%d", originalAppID, i+1))
		}

		return nil
	}

	containerCount := 0

	countContainers := func(_ map[string]interface{}) error {
		containerCount++
		return nil
	}

	i := 0

	injectEntrypoint := func(c map[string]interface{}) error {
		i++

		entrypoint := map[string]interface{}{
			"key":   "entrypoint",
			"value": fmt.Sprintf("/mnt/mesos/sandbox/entrypoint.${count.index * %d + %d}.sh", containerCount, i),
		}

		parametersGroup, ok := c["parameters"].(map[string]interface{})
		if !ok {
			parametersGroup = make(map[string]interface{})
			c["parameters"] = parametersGroup
		}

		parametersGroup["parameter"] = appendSlice(parametersGroup["parameter"], entrypoint)

		return nil
	}

	forEachContainer(app, countContainers)
	forEachContainer(app, injectEntrypoint)

	for i := 0; i < count; i++ {
		for j := 0; j < containerCount; j++ {
			s.Labels = append(s.Labels, fmt.Sprintf("%s-%d-%d", i+1, j))
		}
	}

	return nil
}

func (s *Stack) injectFetchEntrypoints(app map[string]interface{}, metadataCount int) {
	fetch := getSlice(app["fetch"])

	for i := 0; i < metadataCount; i++ {
		fetch = append(fetch, map[string]interface{}{
			"uri":        fmt.Sprintf("%s/entrypoint.%d.sh", s.EntrypointBaseURL, i+1),
			"executable": true,
		})
	}

	app["fetch"] = fetch
}

func (s *Stack) injectHealthChecks(app map[string]interface{}) {
	healthCheckGroup, ok := app["health_checks"].(map[string]interface{})
	if !ok {
		healthCheckGroup = make(map[string]interface{})
		app["health_checks"] = healthCheckGroup
	}

	healthCheckGroup["health_check"] = appendSlice(healthCheckGroup["health_check"], healthCheck)

	containerCount := 0

	injectPortMapping := func(c map[string]interface{}) error {
		containerCount++

		portMappingGroup, ok := c["port_mappings"].(map[string]interface{})
		if !ok {
			portMappingGroup = make(map[string]interface{})
			c["port_mappings"] = portMappingGroup
		}

		portMappingGroup["port_mapping"] = appendSlice(portMappingGroup["port_mapping"], klientPort)

		return nil
	}

	forEachContainer(app, injectPortMapping)

	count, ok := app["count"].(int)
	if !ok {
		count = 1
	}

	count = count * containerCount

	ports := getSlice(app["ports"])

	for ; count > 0; count-- {
		ports = append(ports, 0)
	}

	app["ports"] = ports
}

func (s *Stack) injectMetadata(app map[string]interface{}, labels []string) error {
	envs := getObject(app["env"])

	if val, ok := envs["KODING_KLIENT_URL"].(string); !ok || val == "" {
		envs["KODING_KLIENT_URL"] = s.KlientURL
	}

	for i, label := range labels {
		kiteKey, err := s.BuildKiteKey(label, s.Req.Username)
		if err != nil {
			return err
		}

		konfig := map[string]interface{}{
			"kiteKey":    kiteKey,
			"kontrolURL": stack.Konfig.KontrolURL,
			"kloudURL":   stack.Konfig.KloudURL,
			"tunnelURL":  stack.Konfig.TunnelURL,
		}

		if s.Debug {
			konfig["debug"] = true
		}

		p, err := json.Marshal(map[string]interface{}{"konfig": konfig})
		if err != nil {
			return err
		}

		envs[fmt.Sprintf("KODING_METADATA_%d", i+1)] = base64.StdEncoding.EncodeToString(p)
	}

	app["env"] = envs

	return nil
}

func (s *Stack) plan() (stack.Machines, error) {
	machines := make(stack.Machines, len(s.Labels))

	for _, label := range s.Labels {
		m := &stack.Machine{
			Provider: "marathon",
			Label:    label,
			Attributes: map[string]string{
				"app_id":    strconv.Itoa(s.AppCount),
				"app_count": s.AppOrGroupName,
			},
		}

		machines[label] = m
	}

	return machines, nil
}

// Credential gives Marathon credentials that are attached
// to a current stack.
func (s *Stack) Credential() *Credential {
	return s.BaseStack.Credential.(*Credential)
}

func getObject(v interface{}) map[string]interface{} {
	object := make(map[string]interface{})

	switch v := v.(type) {
	case map[string]interface{}:
		object = v
	case map[string]string:
		for k, v := range v {
			object[k] = v
		}
	}

	return object
}

func getSlice(v interface{}) []interface{} {
	var slice []interface{}

	switch v := v.(type) {
	case nil:
	case []map[string]interface{}:
		slice = make([]interface{}, 0, len(v))

		for _, elem := range v {
			slice = append(slice, elem)
		}
	case []map[string]string:
		slice = make([]interface{}, 0, len(v))

		for _, elem := range v {
			slice = append(slice, elem)
		}
	case []interface{}:
		slice = v
	default:
		slice = []interface{}{v}
	}

	return slice
}

func forEachContainer(app map[string]interface{}, fn func(map[string]interface{}) error) error {
	for _, v := range getSlice(app["container"]) {
		containerGroup, ok := v.(map[string]interface{})
		if !ok {
			continue
		}

		for _, container := range getSlice(containerGroup["docker"]) {
			c, ok := container.(map[string]interface{})
			if !ok {
				continue
			}

			if err := fn(c); err != nil {
				return err
			}
		}
	}

	return nil
}

func appendSlice(slice interface{}, elems ...interface{}) []interface{} {
	return append(getSlice(slice), elems...)
}
