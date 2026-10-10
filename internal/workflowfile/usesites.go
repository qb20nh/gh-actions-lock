package workflowfile

import "gopkg.in/yaml.v3"

type useSite struct {
	node      *yaml.Node
	stepLevel bool
	anchored  bool
}

// walkUseSites shares the dependency locations used by extraction and rewriting.
// Anchored containers matter too: editing their children changes every alias.
func walkUseSites(node *yaml.Node, fn func(useSite)) {
	root, anchored := useContainer(documentRoot(node), false)
	runs, runsAnchored := useContainer(rawMapValue(root, "runs"), anchored)
	walkStepUseSites(runs, runsAnchored, fn)

	jobs, jobsAnchored := useContainer(rawMapValue(root, "jobs"), anchored)
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(jobs.Content); i += 2 {
		job, jobAnchored := useContainer(jobs.Content[i], jobsAnchored)
		if job == nil || job.Kind != yaml.MappingNode {
			continue
		}
		visitUseSite(rawMapValue(job, "uses"), false, jobAnchored, fn)
		walkStepUseSites(job, jobAnchored, fn)
	}
}

func walkStepUseSites(owner *yaml.Node, anchored bool, fn func(useSite)) {
	steps, stepsAnchored := useContainer(rawMapValue(owner, "steps"), anchored)
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return
	}
	for _, stepNode := range steps.Content {
		step, stepAnchored := useContainer(stepNode, stepsAnchored)
		if step == nil || step.Kind != yaml.MappingNode {
			continue
		}
		visitUseSite(rawMapValue(step, "uses"), true, stepAnchored, fn)
	}
}

func visitUseSite(node *yaml.Node, stepLevel, anchored bool, fn func(useSite)) {
	node, anchored = useContainer(node, anchored)
	if node != nil && node.Kind == yaml.ScalarNode {
		fn(useSite{node: node, stepLevel: stepLevel, anchored: anchored})
	}
}

func useContainer(node *yaml.Node, anchored bool) (*yaml.Node, bool) {
	for i := 0; node != nil && i < maxYAMLWalkDepth; i++ {
		anchored = anchored || node.Anchor != "" || node.Kind == yaml.AliasNode
		if node.Kind != yaml.AliasNode {
			return node, anchored
		}
		node = node.Alias
	}
	return nil, anchored
}

func rawMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		if node.Content[i].Kind == yaml.ScalarNode && node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
