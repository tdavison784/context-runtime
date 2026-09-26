package domain

// WouldCreateCycle reports whether adding the edge from -> to would close a
// cycle, given next, which lists the existing successors of a node for the
// same relationship type. Stores use it to keep supersession acyclic
// (FR-REL-004). The walk is iterative so deep chains cannot exhaust the stack.
func WouldCreateCycle(from, to string, next func(id string) ([]string, error)) (bool, error) {
	if from == to {
		return true, nil
	}
	seen := map[string]bool{to: true}
	stack := []string{to}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		succ, err := next(id)
		if err != nil {
			return false, err
		}
		for _, s := range succ {
			if s == from {
				return true, nil
			}
			if !seen[s] {
				seen[s] = true
				stack = append(stack, s)
			}
		}
	}
	return false, nil
}
