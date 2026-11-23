package art

// PlaceAbout explains each place for the legend.
var PlaceAbout = map[string]string{
	"gates":     "Step 1. Tickets arrive here when a job starts, from you or from Pericles.",
	"library":   "Research: the ticket, docs, related work and code, and the ways to check the result.",
	"council":   "Pericles, the product agent, runs the board from here: he reads the open tickets, asks owners about the unclear ones and sends the ready ones to be built.",
	"academy":   "Step 2. Aristotle reads the code and writes the plan.",
	"symposium": "Step 2b, only for hard work. Several approaches on separate branches, scored and compared.",
	"workshop":  "Step 3. Builders write the code.",
	"oracle":    "Step 4. Checks run here. Green smoke means pass, red means fail.",
	"areopagus": "Step 5. Review: hardcoding, overfitting, edge cases and simplicity.",
	"agora":     "The public square. Agents meet here, and wait here when they need you.",
	"theatre":   "The DJ plays sets here. Idle philosophers come to dance.",
	"harbour":   "Step 6. Shipping: commit, push and PR. The ship sails when work goes out.",
	"olympus":   "Home of the gods, above the clouds. Watch for lightning.",
	"horse":     "A wooden horse left on the plain. Nobody knows what is inside.",
	"poseidon":  "The sea god's temple on the cliff at the end of the cape.",
	"pharos":    "The lighthouse. Its fire guides ships home at night.",
	"colossus":  "A bronze giant standing over the island harbour, torch raised.",
}

// PeopleAbout says what each character does, for the legend.
var PeopleAbout = map[string]string{
	"pericles":     "Runs the board: fixes tickets, asks owners, starts jobs on ready tickets.",
	"herodotus":    "Digs through docs, history and related tickets.",
	"thales":       "Finds the ways to check the result.",
	"anaximander":  "Maps the codebase.",
	"eratosthenes": "Measures scope, sizes and test run times.",
	"aristotle":    "Turns research into a structured plan.",
	"plato":        "Weighs approaches and proposes experiments.",
	"archimedes":   "Builds the main change.",
	"hero":         "Builds experiments and small tools.",
	"euclid":       "Makes careful, exact changes.",
	"pythagoras":   "Fixes failing checks.",
	"socrates":     "Asks whether the work is really done.",
	"diogenes":     "Hunts hardcoded values and shortcuts.",
	"zeno":         "Looks for edge cases.",
	"heraclitus":   "Keeps the code simple.",
	"protagoras":   "Rewrites prompts and comments in plain language.",
	"isocrates":    "Rewrites docs and PR text plainly.",
	"xenophon":     "Writes it up and ships it.",
	"hermes":       "Carries news between agents and keeps the board in step.",
	"homer":        "The narrator. Tells you what is happening in the city and why.",
	"solomun":      "DJ. Plays sets at the Theatre.",
}
