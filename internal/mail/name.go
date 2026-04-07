package mail

import (
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/sunls24/gox"
)

var firstNames = []string{
	"Emma", "Olivia", "Ava", "Sophia", "Isabella",
	"Mia", "Amelia", "Harper", "Evelyn", "Abigail",
	"Emily", "Ella", "Elizabeth", "Camila", "Luna",
	"Sofia", "Avery", "Mila", "Aria", "Scarlett",
	"Penelope", "Layla", "Chloe", "Victoria", "Madison",
	"Eleanor", "Grace", "Nora", "Riley", "Zoey",
	"Hannah", "Hazel", "Lily", "Ellie", "Violet",
	"Lillian", "Zoe", "Stella", "Aurora", "Natalie",
	"Emilia", "Everly", "Leah", "Aubrey", "Willow",
	"Addison", "Lucy", "Audrey", "Bella", "Nova",
	"Brooklyn", "Paisley", "Savannah", "Claire", "Skylar",
	"Isla", "Genesis", "Naomi", "Elena", "Caroline",
	"Anna", "Maya", "Valentina", "Ruby", "Kennedy",
	"Ivy", "Ariana", "Aaliyah", "Cora", "Madelyn",
	"Alice", "Kinsley", "Hailey", "Gabriella", "Allison",
	"Gianna", "Serenity", "Samantha", "Sarah", "Autumn",
	"Quinn", "Eva", "Piper", "Sophie", "Sadie",
	"Delilah", "Josephine", "Nevaeh", "Adeline", "Arya",
	"Emery", "Lydia", "Clara", "Vivian", "Madeline",
	"Liam", "Noah", "Oliver", "Elijah", "James",
	"William", "Benjamin", "Lucas", "Henry", "Alexander",
	"Mason", "Michael", "Ethan", "Daniel", "Jacob",
	"Logan", "Jackson", "Levi", "Sebastian", "Mateo",
	"Jack", "Owen", "Theodore", "Aiden", "Samuel",
	"Joseph", "John", "David", "Wyatt", "Matthew",
	"Luke", "Asher", "Carter", "Julian", "Grayson",
	"Leo", "Jayden", "Gabriel", "Isaac", "Lincoln",
	"Anthony", "Hudson", "Dylan", "Ezra", "Thomas",
	"Charles", "Christopher", "Jaxon", "Maverick", "Josiah",
	"Isaiah", "Andrew", "Elias", "Joshua", "Nathan",
	"Caleb", "Ryan", "Adrian", "Miles", "Eli",
	"Nolan", "Christian", "Aaron", "Cameron", "Ezekiel",
	"Colton", "Luca", "Landon", "Hunter", "Jonathan",
	"Santiago", "Axel", "Easton", "Cooper", "Jeremiah",
	"Angel", "Roman", "Connor", "Jameson", "Robert",
	"Greyson", "Jordan", "Ian", "Carson", "Jaxson",
	"Leonardo", "Nicholas", "Dominic", "Austin", "Everett",
	"Brooks", "Xavier", "Kai", "Jose", "Parker",
	"Adam", "Jace", "Wesley", "Kayden", "Silas",
}
var lastNames = []string{
	"Smith", "Johnson", "Williams", "Brown", "Jones",
	"Garcia", "Miller", "Davis", "Rodriguez", "Martinez",
	"Hernandez", "Lopez", "Gonzalez", "Wilson", "Anderson",
	"Thomas", "Taylor", "Moore", "Jackson", "Martin",
	"Lee", "Perez", "Thompson", "White", "Harris",
	"Sanchez", "Clark", "Ramirez", "Lewis", "Robinson",
	"Walker", "Young", "Allen", "King", "Wright",
	"Scott", "Green", "Baker", "Adams", "Nelson",
	"Hill", "Campbell", "Mitchell", "Roberts", "Carter",
	"Phillips", "Evans", "Turner", "Torres", "Parker",
	"Collins", "Edwards", "Stewart", "Flores", "Morris",
	"Nguyen", "Murphy", "Rivera", "Cook", "Rogers",
	"Morgan", "Peterson", "Cooper", "Reed", "Bailey",
	"Bell", "Gomez", "Kelly", "Howard", "Ward",
	"Cox", "Diaz", "Richardson", "Wood", "Watson",
	"Brooks", "Bennett", "Gray", "James", "Reyes",
	"Cruz", "Hughes", "Price", "Myers", "Long",
	"Foster", "Sanders", "Ross", "Morales", "Powell",
	"Sullivan", "Russell", "Ortiz", "Jenkins", "Gutierrez",
	"Perry", "Butler", "Barnes", "Fisher", "Henderson",
	"Coleman", "Simmons", "Patterson", "Jordan", "Reynolds",
	"Hamilton", "Graham", "Kim", "Gonzales", "Alexander",
	"Ramos", "Wallace", "Griffin", "West", "Cole",
	"Hayes", "Chavez", "Gibson", "Bryant", "Ellis",
	"Stevens", "Murray", "Ford", "Marshall", "Owens",
	"Mcdonald", "Harrison", "Ruiz", "Kennedy", "Wells",
	"Alvarez", "Woods", "Mendoza", "Castillo", "Olson",
	"Webb", "Washington", "Tucker", "Freeman", "Burns",
	"Henry", "Vasquez", "Snyder", "Simpson", "Crawford",
	"Jimenez", "Porter", "Mason", "Shaw", "Gordon",
	"Wagner", "Hunter", "Romero", "Hicks", "Dixon",
	"Hunt", "Palmer", "Robertson", "Black", "Holmes",
	"Stone", "Meyer", "Boyd", "Mills", "Warren",
	"Fox", "Rose", "Rice", "Moreno", "Schmidt",
	"Patel", "Ferguson", "Nichols", "Herrera", "Medina",
	"Ryan", "Fernandez", "Weaver", "Daniels", "Stephens",
	"Gardner", "Payne", "Kelley", "Dunn", "Pierce",
	"Arnold", "Tran", "Spencer", "Peters", "Hawkins",
	"Grant", "Hansen", "Castro", "Hoffman", "Hart",
	"Elliott", "Cunningham", "Knight", "Bradley", "Carroll",
	"Hudson", "Duncan", "Armstrong", "Berry", "Andrews",
}

func GenerateName() string {
	first := firstNames[rand.IntN(len(firstNames))]
	last := lastNames[rand.IntN(len(lastNames))]
	return first + " " + last
}

var connection = []string{"_", "-", "."}

func nameToAddress(name string) (address string) {
	if name == "" {
		address = gox.RandStr(8)
	} else {
		address = strings.ReplaceAll(name, " ", connection[rand.IntN(len(connection))])
		if r := rand.IntN(200) + 1; r < 100 {
			address += strconv.Itoa(r)
		}
	}
	return strings.ToLower(address)
}
