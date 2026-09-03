package diagram

import "testing"

func TestValidMermaidPasses(t *testing.T) {
	sources := map[string]string{
		"sequence": `sequenceDiagram
    Browser->>API: POST /checkout
    API->>OrderService: createOrder()
    OrderService->>Database: persist order
    OrderService-->>API: order id
    API-->>Browser: 201 Created`,
		"flowchart": `flowchart LR
    Browser --> API
    API --> Service
    Service --> Database`,
		"sequence with blocks": `sequenceDiagram
    participant Worker
    autonumber
    alt payment authorised
        Worker->>Ledger: record payment
    else declined
        Worker->>Queue: publish PaymentFailed
    end`,
		"subgraphs": `flowchart TB
    subgraph Ingress
        API --> Router
    end
    Router --> Service`,
	}
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			if issues := ValidateMermaid(src); Fatal(issues) {
				t.Errorf("valid diagram rejected: %v", issues)
			}
		})
	}
}

func TestInvalidMermaidIsDetected(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"header only":         "sequenceDiagram",
		"missing label":       "sequenceDiagram\n    Browser->>API",
		"missing recipient":   "sequenceDiagram\n    Browser->>: hello",
		"not a message":       "sequenceDiagram\n    Browser and API talk",
		"unbalanced brackets": "flowchart LR\n    A[Start --> B[End]",
		"unclosed subgraph":   "flowchart TB\n    subgraph Ingress\n    API --> Router",
		"no edges":            "flowchart LR\n    JustOneNode",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			issues := ValidateMermaid(src)
			if !Fatal(issues) {
				t.Errorf("invalid diagram accepted: %v", issues)
			}
		})
	}
}

func TestBracketsInsideLabelsAreLiteral(t *testing.T) {
	src := `flowchart LR
    A["order[0] (first)"] --> B["done"]`
	if issues := ValidateMermaid(src); Fatal(issues) {
		t.Errorf("brackets inside quoted labels should not be treated as structure: %v", issues)
	}
}

func TestUnknownDiagramTypeIsNotFatal(t *testing.T) {
	// Mermaid gains diagram types faster than this validator does, so an
	// unfamiliar header is reported without failing the walkthrough.
	issues := ValidateMermaid("quadrantChart\n    title Reach\n    A: [0.3, 0.6]")
	if Fatal(issues) {
		t.Errorf("unknown diagram type should not be fatal: %v", issues)
	}
	if len(issues) == 0 {
		t.Error("unknown diagram type should still be reported")
	}
}
