package config

import "testing"

func TestValidateGlusterClusterCheck(t *testing.T) {
	good := validateService(t, `
name: gluster-node
watches:
  cluster:
    check:
      type: gluster_cluster
      peers: [zeus, apolo]
      volumes:
        images:
          bricks: 3
          self_heal: true
          max_heal_entries: 0
          max_split_brain_entries: 0
`)
	mustNotHave(t, good, "gluster_cluster")

	bad := validateService(t, `
name: gluster-node
watches:
  cluster:
    check:
      type: gluster_cluster
      peers: zeus
      volumes:
        bad/name:
          bricks: 0
          self_heal: yes
          max_heal_entries: -1
          max_split_brain_entries: bad
          unexpected: true
`)
	for _, want := range []string{
		"checks.cluster.peers must be a non-empty list of names",
		"checks.cluster.volumes volume name \"bad/name\" is unsafe",
		"checks.cluster.volumes.bad/name.bricks is required and must be a positive integer",
		"checks.cluster.volumes.bad/name.self_heal must be a boolean",
		"checks.cluster.volumes.bad/name.max_heal_entries must be a non-negative integer",
		"checks.cluster.volumes.bad/name.max_split_brain_entries must be a non-negative integer",
		"checks.cluster.volumes.bad/name.unexpected is not supported",
	} {
		mustHave(t, bad, want)
	}
}
