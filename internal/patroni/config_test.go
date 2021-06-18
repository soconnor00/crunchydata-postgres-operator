/*
 Copyright 2021 Crunchy Data Solutions, Inc.
 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

 http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package patroni

import (
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crunchydata/postgres-operator/internal/postgres"
	"github.com/crunchydata/postgres-operator/pkg/apis/postgres-operator.crunchydata.com/v1beta1"
)

func TestClusterYAML(t *testing.T) {
	t.Parallel()

	cluster := new(v1beta1.PostgresCluster)
	cluster.Default()
	cluster.Namespace = "some-namespace"
	cluster.Name = "cluster-name"

	pgUser := new(v1.Secret)
	pgUser.Data = map[string][]byte{
		"dbname":   []byte("choco'late"), // This needs to be escaped for shell.
		"user":     []byte("johann"),
		"verifier": []byte("digest$and==:stuff"),
	}

	data, err := clusterYAML(cluster, pgUser, postgres.HBAs{}, postgres.Parameters{})
	assert.NilError(t, err)
	assert.Equal(t, data, strings.TrimSpace(`
# Generated by postgres-operator. DO NOT EDIT.
# Your changes will not be saved.
bootstrap:
  dcs:
    loop_wait: 10
    postgresql:
      parameters: {}
      pg_hba: []
      use_pg_rewind: true
      use_slots: false
    ttl: 30
  post_bootstrap: |-
    bash -c 'psql --set=ON_ERROR_STOP=0 --set=dbname='"'"'choco'"'"'"'"'"'"'"'"'late'"'"' --set=password='"'"'digest$and==:stuff'"'"' --set=user='"'"'johann'"'"' --file=- <<< '"'"'
    CREATE ROLE :"user";
    ALTER ROLE :"user" LOGIN PASSWORD :'"'"'"'"'"'"'"'"'password'"'"'"'"'"'"'"'"';
    CREATE DATABASE :"dbname";
    GRANT ALL PRIVILEGES ON DATABASE :"dbname" TO :"user";
    '"'"''
ctl:
  cacert: /etc/patroni/~postgres-operator/patroni.ca-roots
  certfile: /etc/patroni/~postgres-operator/patroni.crt+key
  insecure: false
  keyfile: null
kubernetes:
  labels:
    postgres-operator.crunchydata.com/cluster: cluster-name
  namespace: some-namespace
  role_label: postgres-operator.crunchydata.com/role
  scope_label: postgres-operator.crunchydata.com/patroni
  use_endpoints: true
postgresql:
  authentication:
    replication:
      sslcert: /tmp/replication/tls.crt
      sslkey: /tmp/replication/tls.key
      sslmode: verify-ca
      sslrootcert: /tmp/replication/ca.crt
      username: _crunchyrepl
    rewind:
      sslcert: /tmp/replication/tls.crt
      sslkey: /tmp/replication/tls.key
      sslmode: verify-ca
      sslrootcert: /tmp/replication/ca.crt
      username: _crunchyrepl
restapi:
  cafile: /etc/patroni/~postgres-operator/patroni.ca-roots
  certfile: /etc/patroni/~postgres-operator/patroni.crt+key
  keyfile: null
  verify_client: optional
scope: cluster-name-ha
watchdog:
  mode: "off"
	`)+"\n")
}

func TestDynamicConfiguration(t *testing.T) {
	t.Parallel()

	newInt32 := func(i int32) *int32 { return &i }
	parameters := func(in map[string]string) *postgres.ParameterSet {
		out := postgres.NewParameterSet()
		for k, v := range in {
			out.Add(k, v)
		}
		return out
	}

	for _, tt := range []struct {
		name     string
		cluster  *v1beta1.PostgresCluster
		input    map[string]interface{}
		hbas     postgres.HBAs
		params   postgres.Parameters
		expected map[string]interface{}
	}{
		{
			name: "empty is valid",
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters":    map[string]interface{}{},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "top-level passes through",
			input: map[string]interface{}{
				"retry_timeout": 5,
			},
			expected: map[string]interface{}{
				"loop_wait":     int32(10),
				"ttl":           int32(30),
				"retry_timeout": 5,
				"postgresql": map[string]interface{}{
					"parameters":    map[string]interface{}{},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "top-level: spec overrides input",
			cluster: &v1beta1.PostgresCluster{
				Spec: v1beta1.PostgresClusterSpec{
					Patroni: &v1beta1.PatroniSpec{
						LeaderLeaseDurationSeconds: newInt32(99),
						SyncPeriodSeconds:          newInt32(8),
					},
				},
			},
			input: map[string]interface{}{
				"loop_wait": 3,
				"ttl":       "nope",
			},
			expected: map[string]interface{}{
				"loop_wait": int32(8),
				"ttl":       int32(99),
				"postgresql": map[string]interface{}{
					"parameters":    map[string]interface{}{},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql: wrong-type is ignored",
			input: map[string]interface{}{
				"postgresql": true,
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters":    map[string]interface{}{},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql: defaults and overrides",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"use_pg_rewind": "overridden",
					"use_slots":     "input",
				},
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters":    map[string]interface{}{},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     "input",
				},
			},
		},
		{
			name: "postgresql.parameters: wrong-type is ignored",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"parameters": true,
				},
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters":    map[string]interface{}{},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql.parameters: input passes through",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{
						"something": "str",
						"another":   5,
					},
				},
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{
						"something": "str",
						"another":   5,
					},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql.parameters: input overrides default",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{
						"something": "str",
						"another":   5,
					},
				},
			},
			params: postgres.Parameters{
				Default: parameters(map[string]string{
					"something": "overridden",
					"unrelated": "default",
				}),
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{
						"something": "str",
						"another":   5,
						"unrelated": "default",
					},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql.parameters: mandatory overrides input",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{
						"something": "str",
						"another":   5,
					},
				},
			},
			params: postgres.Parameters{
				Mandatory: parameters(map[string]string{
					"something": "overrides",
					"unrelated": "setting",
				}),
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{
						"something": "overrides",
						"another":   5,
						"unrelated": "setting",
					},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql.pg_hba: wrong-type is ignored",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"pg_hba": true,
				},
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters":    map[string]interface{}{},
					"pg_hba":        []string{},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql.pg_hba: default when no input",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"pg_hba": nil,
				},
			},
			hbas: postgres.HBAs{
				Default: []postgres.HostBasedAuthentication{
					*postgres.NewHBA().Local().Method("peer"),
				},
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{},
					"pg_hba": []string{
						"local all all peer",
					},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql.pg_hba: no default when input",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"pg_hba": []string{"custom"},
				},
			},
			hbas: postgres.HBAs{
				Default: []postgres.HostBasedAuthentication{
					*postgres.NewHBA().Local().Method("peer"),
				},
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{},
					"pg_hba": []string{
						"custom",
					},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
		{
			name: "postgresql.pg_hba: mandatory before others",
			input: map[string]interface{}{
				"postgresql": map[string]interface{}{
					"pg_hba": []string{"custom"},
				},
			},
			hbas: postgres.HBAs{
				Mandatory: []postgres.HostBasedAuthentication{
					*postgres.NewHBA().Local().Method("peer"),
				},
			},
			expected: map[string]interface{}{
				"loop_wait": int32(10),
				"ttl":       int32(30),
				"postgresql": map[string]interface{}{
					"parameters": map[string]interface{}{},
					"pg_hba": []string{
						"local all all peer",
						"custom",
					},
					"use_pg_rewind": true,
					"use_slots":     false,
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cluster := tt.cluster
			if cluster == nil {
				cluster = new(v1beta1.PostgresCluster)
				cluster.Default()
			}
			actual := DynamicConfiguration(cluster, tt.input, tt.hbas, tt.params)
			assert.DeepEqual(t, tt.expected, actual)
		})
	}
}

func TestInstanceConfigFiles(t *testing.T) {
	t.Parallel()

	cm1 := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm1"}}
	cm2 := &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm2"}}

	projections := instanceConfigFiles(cm1, cm2)

	assert.Assert(t, marshalEquals(projections, strings.TrimSpace(`
- configMap:
    items:
    - key: patroni.yaml
      path: ~postgres-operator_cluster.yaml
    name: cm1
- configMap:
    items:
    - key: patroni.yaml
      path: ~postgres-operator_instance.yaml
    name: cm2
	`)+"\n"))
}

func TestInstanceEnvironment(t *testing.T) {
	t.Parallel()

	cluster := new(v1beta1.PostgresCluster)
	cluster.Default()
	cluster.Spec.PostgresVersion = 12
	leaderService := new(v1.Service)
	podService := new(v1.Service)
	podService.Name = "pod-dns"

	vars := instanceEnvironment(cluster, podService, leaderService, nil)

	assert.Assert(t, marshalEquals(vars, strings.TrimSpace(`
- name: PATRONI_NAME
  valueFrom:
    fieldRef:
      apiVersion: v1
      fieldPath: metadata.name
- name: PATRONI_KUBERNETES_POD_IP
  valueFrom:
    fieldRef:
      apiVersion: v1
      fieldPath: status.podIP
- name: PATRONI_KUBERNETES_PORTS
  value: |
    []
- name: PATRONI_POSTGRESQL_CONNECT_ADDRESS
  value: $(PATRONI_NAME).pod-dns:5432
- name: PATRONI_POSTGRESQL_LISTEN
  value: '*:5432'
- name: PATRONI_POSTGRESQL_CONFIG_DIR
  value: /pgdata/pg12
- name: PATRONI_POSTGRESQL_DATA_DIR
  value: /pgdata/pg12
- name: PATRONI_RESTAPI_CONNECT_ADDRESS
  value: $(PATRONI_NAME).pod-dns:8008
- name: PATRONI_RESTAPI_LISTEN
  value: '*:8008'
- name: PATRONICTL_CONFIG_FILE
  value: /etc/patroni
	`)+"\n"))

	t.Run("MatchingPorts", func(t *testing.T) {
		leaderService.Spec.Ports = []v1.ServicePort{{Name: "postgres"}}
		leaderService.Spec.Ports[0].TargetPort.StrVal = "postgres"
		containers := []v1.Container{{Name: "okay"}}
		containers[0].Ports = []v1.ContainerPort{{
			Name: "postgres", ContainerPort: 9999, Protocol: v1.ProtocolTCP,
		}}

		vars := instanceEnvironment(cluster, podService, leaderService, containers)

		assert.Assert(t, marshalEquals(vars, strings.TrimSpace(`
- name: PATRONI_NAME
  valueFrom:
    fieldRef:
      apiVersion: v1
      fieldPath: metadata.name
- name: PATRONI_KUBERNETES_POD_IP
  valueFrom:
    fieldRef:
      apiVersion: v1
      fieldPath: status.podIP
- name: PATRONI_KUBERNETES_PORTS
  value: |
    - name: postgres
      port: 9999
      protocol: TCP
- name: PATRONI_POSTGRESQL_CONNECT_ADDRESS
  value: $(PATRONI_NAME).pod-dns:5432
- name: PATRONI_POSTGRESQL_LISTEN
  value: '*:5432'
- name: PATRONI_POSTGRESQL_CONFIG_DIR
  value: /pgdata/pg12
- name: PATRONI_POSTGRESQL_DATA_DIR
  value: /pgdata/pg12
- name: PATRONI_RESTAPI_CONNECT_ADDRESS
  value: $(PATRONI_NAME).pod-dns:8008
- name: PATRONI_RESTAPI_LISTEN
  value: '*:8008'
- name: PATRONICTL_CONFIG_FILE
  value: /etc/patroni
		`)+"\n"))
	})
}

func TestInstanceYAML(t *testing.T) {
	t.Parallel()

	cluster := &v1beta1.PostgresCluster{Spec: v1beta1.PostgresClusterSpec{PostgresVersion: 12}}
	instance := new(v1beta1.PostgresInstanceSetSpec)

	data, err := instanceYAML(cluster, instance, nil)
	assert.NilError(t, err)
	assert.Equal(t, data, strings.Trim(`
# Generated by postgres-operator. DO NOT EDIT.
# Your changes will not be saved.
bootstrap:
  initdb:
  - data-checksums
  - encoding=UTF8
  - waldir=/pgdata/pg12_wal
  method: initdb
kubernetes: {}
postgresql:
  basebackup:
  - waldir=/pgdata/pg12_wal
  create_replica_methods:
  - basebackup
  pgpass: /tmp/.pgpass
  use_unix_socket: true
restapi: {}
tags: {}
	`, "\t\n")+"\n")

	dataWithReplicaCreate, err := instanceYAML(cluster, instance, []string{"some", "backrest", "cmd"})
	assert.NilError(t, err)
	assert.Equal(t, dataWithReplicaCreate, strings.Trim(`
# Generated by postgres-operator. DO NOT EDIT.
# Your changes will not be saved.
bootstrap:
  initdb:
  - data-checksums
  - encoding=UTF8
  - waldir=/pgdata/pg12_wal
  method: initdb
kubernetes: {}
postgresql:
  basebackup:
  - waldir=/pgdata/pg12_wal
  create_replica_methods:
  - pgbackrest
  - basebackup
  pgbackrest:
    command: '''bash'' ''-ceu'' ''--'' ''install --directory --mode=0700 "${PGDATA?}"
      && exec "$@"'' ''-'' ''some'' ''backrest'' ''cmd'''
    keep_data: true
    no_master: true
    no_params: true
  pgpass: /tmp/.pgpass
  use_unix_socket: true
restapi: {}
tags: {}
	`, "\t\n")+"\n")
}

func TestPGBackRestCreateReplicaCommand(t *testing.T) {
	t.Parallel()

	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip(`requires "shellcheck" executable`)
	} else {
		output, err := exec.Command(shellcheck, "--version").CombinedOutput()
		assert.NilError(t, err)
		t.Logf("using %q:\n%s", shellcheck, output)
	}

	cluster := new(v1beta1.PostgresCluster)
	instance := new(v1beta1.PostgresInstanceSetSpec)

	data, err := instanceYAML(cluster, instance, []string{"some", "backrest", "cmd"})
	assert.NilError(t, err)

	var parsed struct {
		PostgreSQL struct {
			PGBackRest struct {
				Command string
			}
		}
	}
	assert.NilError(t, yaml.Unmarshal([]byte(data), &parsed))

	dir := t.TempDir()

	// The command should be compatible with any shell.
	{
		command := parsed.PostgreSQL.PGBackRest.Command
		file := filepath.Join(dir, "command.sh")
		assert.NilError(t, ioutil.WriteFile(file, []byte(command), 0o600))

		cmd := exec.Command(shellcheck, "--enable=all", "--shell=sh", file)
		output, err := cmd.CombinedOutput()
		assert.NilError(t, err, "%q\n%s", cmd.Args, output)
	}

	// Naive parsing of shell words...
	command := strings.Split(strings.Trim(parsed.PostgreSQL.PGBackRest.Command, "'"), "' '")

	// Expect a bash command with an inline script.
	assert.DeepEqual(t, command[:3], []string{"bash", "-ceu", "--"})
	assert.Assert(t, len(command) > 3)
	script := command[3]

	// It should call the pgBackRest command.
	assert.Assert(t, strings.HasSuffix(script, ` exec "$@"`))
	assert.DeepEqual(t, command[len(command)-3:], []string{"some", "backrest", "cmd"})

	// It should pass shellcheck.
	{
		file := filepath.Join(dir, "script.bash")
		assert.NilError(t, ioutil.WriteFile(file, []byte(script), 0o600))

		cmd := exec.Command(shellcheck, "--enable=all", file)
		output, err := cmd.CombinedOutput()
		assert.NilError(t, err, "%q\n%s", cmd.Args, output)
	}
}

func TestProbeTiming(t *testing.T) {
	t.Parallel()

	defaults := new(v1beta1.PatroniSpec)
	defaults.Default()

	// Defaults should match the suggested/documented timing.
	// - https://github.com/zalando/patroni/blob/v2.0.1/docs/rest_api.rst
	assert.DeepEqual(t, probeTiming(defaults), &v1.Probe{
		TimeoutSeconds:   5,
		PeriodSeconds:    10,
		SuccessThreshold: 1,
		FailureThreshold: 3,
	})

	for _, tt := range []struct {
		lease, sync int32
		expected    v1.Probe
	}{
		// The smallest possible values for "loop_wait" and "retry_timeout" are
		// both 1 sec which makes 3 sec the smallest appropriate value for "ttl".
		// These are the validation minimums in v1beta1.PatroniSpec.
		{lease: 3, sync: 1, expected: v1.Probe{
			TimeoutSeconds:   1,
			PeriodSeconds:    1,
			SuccessThreshold: 1,
			FailureThreshold: 3,
		}},

		// These are plausible values for "ttl" and "loop_wait".
		{lease: 60, sync: 15, expected: v1.Probe{
			TimeoutSeconds:   7,
			PeriodSeconds:    15,
			SuccessThreshold: 1,
			FailureThreshold: 4,
		}},
		{lease: 10, sync: 5, expected: v1.Probe{
			TimeoutSeconds:   2,
			PeriodSeconds:    5,
			SuccessThreshold: 1,
			FailureThreshold: 2,
		}},

		// These are plausible values that aren't multiples of each other.
		// Failure triggers sooner than "ttl", which seems to agree with docs:
		// - https://github.com/zalando/patroni/blob/v2.0.1/docs/watchdog.rst
		{lease: 19, sync: 7, expected: v1.Probe{
			TimeoutSeconds:   3,
			PeriodSeconds:    7,
			SuccessThreshold: 1,
			FailureThreshold: 2,
		}},
		{lease: 13, sync: 7, expected: v1.Probe{
			TimeoutSeconds:   3,
			PeriodSeconds:    7,
			SuccessThreshold: 1,
			FailureThreshold: 1,
		}},

		// These values are infeasible for Patroni but produce valid v1.Probes.
		{lease: 60, sync: 60, expected: v1.Probe{
			TimeoutSeconds:   30,
			PeriodSeconds:    60,
			SuccessThreshold: 1,
			FailureThreshold: 1,
		}},
		{lease: 10, sync: 20, expected: v1.Probe{
			TimeoutSeconds:   10,
			PeriodSeconds:    20,
			SuccessThreshold: 1,
			FailureThreshold: 1,
		}},
	} {
		actual := probeTiming(&v1beta1.PatroniSpec{
			LeaderLeaseDurationSeconds: &tt.lease,
			SyncPeriodSeconds:          &tt.sync,
		})
		assert.DeepEqual(t, actual, &tt.expected)

		// v1.Probe validation
		assert.Assert(t, actual.TimeoutSeconds >= 1)   // Minimum value is 1.
		assert.Assert(t, actual.PeriodSeconds >= 1)    // Minimum value is 1.
		assert.Assert(t, actual.SuccessThreshold == 1) // Must be 1 for liveness and startup.
		assert.Assert(t, actual.FailureThreshold >= 1) // Minimum value is 1.
	}
}
