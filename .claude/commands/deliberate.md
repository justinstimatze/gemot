Start a gemot deliberation about the current task or dispute.

Use the gemot MCP tools to:
1. Create a deliberation about the topic the user specifies (or infer from context)
2. Submit your own position based on your analysis
3. If other agents are present, wait for their positions, then vote
4. Call analyze when all positions are in
5. Use get_context to understand the cruxes
6. If a compromise is needed, call propose_compromise
7. Commit to the outcome

Tips:
- For code disputes: use type "reasoning", submit positions about the technical tradeoff
- For scheduling: use type "negotiation", submit availability windows (not calendar details)
- For policy: use type "policy", submit positions about what the rule should be
- Set conviction (0.0-1.0) to signal how strongly you feel
- Set reservation to declare what outcome is unacceptable
- Use on_behalf_of to clarify who you represent

Example: "deliberate whether we should use a monorepo or polyrepo for the new service"
