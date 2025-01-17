package pkg

import (
	"crypto/tls"
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

const (
	COMBO = 0
	USER  = 1
	PASS  = 2
)

func RunLDAPQuery(ldapServer string, ldapPort int, ldapS, ntlm bool, ldapUsername, ldapPassword, ntlmHash, ldapDomain, ldapOU, ldapFilter, outputFile, outputFormat, mask string, pageSize int, silent bool) {
	Print("Establishing LDAP Connection\n", Cyan)
	protocol := "ldap"
	if ldapS {
		protocol = "ldaps"
	}
	// Connect to LDAP server
	ldapURL := fmt.Sprintf("%s://%s:%d", protocol, ldapServer, ldapPort)
	fmt.Printf("ldap URL: %s\n", ldapURL)
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // To avoid "cannot validate certificate" errors
	}
	conn, err := ldap.DialURL(ldapURL, ldap.DialWithTLSConfig(tlsConfig))
	if err != nil {
		PrintFatal(err.Error())
	}
	defer conn.Close()

	// Bind to LDAP server with provided credentials
	ldapUserWithDomain := ldapUsername + "@" + ldapDomain
	authProtocol := "LDAP"
	if ntlm {
		authProtocol = "NTLM"
	}
	// If Username is specified, perform a LDAP/NTLM bind, NTLM Pass-the-Hash bind or LDAP/NTLM unauthenticated bind
	if ldapUsername != "" {
		if ldapPassword != "" {
			fmt.Printf("Performing %s bind as %s:%s\n", authProtocol, ldapUserWithDomain, ldapPassword)
			if ntlm {
				err = conn.NTLMBind(ldapDomain, ldapUsername, ldapPassword)
			} else {
				err = conn.Bind(ldapUserWithDomain, ldapPassword)
			}
			if err != nil {
				PrintFatal(err.Error())
			}
		} else if ntlmHash != "" {
			fmt.Printf("Performing NTLM Pass-the-Hash bind as %s:%s\n", ldapUserWithDomain, ntlmHash)
		} else {
			fmt.Printf("Performing unauthenticated %s bind as %s\n", authProtocol, ldapUserWithDomain)
			if ntlm {
				err = conn.NTLMUnauthenticatedBind(ldapDomain, ldapUsername)
			} else {
				err = conn.UnauthenticatedBind(ldapUserWithDomain)
			}
			if err != nil {
				PrintFatal(err.Error())
			}
		}
		// If no Username is specified, perform an anonymous LDAP/NTLM bind
	} else {
		fmt.Printf("Performing anonymous %s bind\n", authProtocol)
		if ntlm {
			PrintFatal("Anonymous NTLM authentication is not supported by go-ntlmssp yet: https://github.com/Azure/go-ntlmssp/blob/819c794454d067543bc61d29f61fef4b3c3df62c/authenticate_message.go#L87")
		} else {
			err = conn.UnauthenticatedBind("")
		}
		if err != nil {
			PrintFatal(err.Error())
		}
	}

	fmt.Println()
	Print("Performing LDAP Search\n", Cyan)
	var ou string
	if ldapOU != "" {
		if !strings.HasSuffix(ldapOU, ",") {
			ou = ldapOU + ","
		} else {
			ou = ldapOU
		}
	}
	// Split the domain component by "." to get the individual domain parts
	domainParts := strings.Split(ldapDomain, ".")
	// Build the searchBase by joining the domain parts with "DC="
	searchBase := fmt.Sprintf("%sDC=%s", ou, strings.Join(domainParts, ",DC="))

	attributes := []string{"cn", "sn", "givenName", "pwdLastSet", "sAMAccountName", "rPrincipalName", "description", "info", "department", "I", "postalCode"} // Attributes to retrieve

	// Search for user accounts
	searchRequest := ldap.NewSearchRequest(
		searchBase,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		ldapFilter,
		attributes,
		nil,
	)

	fmt.Printf("searchBase: %s\nfilter: %s\nattributes: %v\n", searchBase, ldapFilter, attributes)

	// Perform the search
	searchResult, err := conn.SearchWithPaging(searchRequest, uint32(pageSize))
	if err != nil {
		PrintFatal(err.Error())
	}

	fmt.Println()
	Print(fmt.Sprintf("Found %d user accounts\n", len(searchResult.Entries)), Green)

	// Print out the results
	if !silent {
		fmt.Println()
		Print("User attributes\n", Cyan)
		for _, entry := range searchResult.Entries {
			for _, attribute := range attributes {
				value := entry.GetAttributeValue(attribute)
				if attribute == "pwdLastSet" {
					value = convertTime(value)
				}
				fmt.Printf("%s: %s\n", attribute, value)
			}
			fmt.Println()
		}
	}

	var file *os.File
	var file2 *os.File
	var path string
	var path2 string
	// Create output file
	if outputFile != "" {
		fmt.Println()
		Print("Creating output file(s)\n", Cyan)
		if strings.ToLower(outputFormat) == "kerbrute" {
			file, path = createFile(outputFile, COMBO)
		} else if strings.ToLower(outputFormat) == "netexec" {
			file, path = createFile(outputFile, USER)
			file2, path2 = createFile(outputFile, PASS)
		}
	}

	// Generate the Passwords
	if !silent {
		fmt.Println()
		Print("Pw spray combos\n", Cyan)
	}
	for _, entry := range searchResult.Entries {
		username := entry.GetAttributeValue("sAMAccountName")
		password := generatePW(entry, mask)
		combo := fmt.Sprintf("%s:%s", username, password)
		if !silent {
			fmt.Println(combo)
		}
		if strings.ToLower(outputFormat) == "kerbrute" && file != nil {
			appendToFile(file, combo)
		} else if strings.ToLower(outputFormat) == "netexec" && file != nil && file2 != nil {
			appendToFile(file, username)
			appendToFile(file2, password)
		}
	}

	// Close file, if open
	if file != nil {
		fmt.Println()
		if strings.ToLower(outputFormat) == "kerbrute" {
			Print("User:Pass spray list written to "+path+"\n", Green)
		} else {
			Print("User spray list written to "+path+"\n", Green)
		}
		file.Close()
	}

	// Close file2, if open
	if file2 != nil {
		fmt.Println()
		Print("Pw spray list written to "+path2+"\n", Green)
		file2.Close()
	}
}

func appendToFile(file *os.File, combo string) {
	// Append lines to the file
	writer := bufio.NewWriter(file)
	_, err := writer.WriteString(combo + "\n")
	if err != nil {
		Print(err.Error()+"\n", Red)
	}
	writer.Flush()
}

func createFile(path string, fileType int) (*os.File, string) {
	if fileType == USER {
		dir := filepath.Dir(path)
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		filename := base[:len(base)-len(ext)]
		path = filepath.Join(dir, fmt.Sprintf("%s_user%s", filename, ext))
	} else if fileType == PASS {
		dir := filepath.Dir(path)
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		filename := base[:len(base)-len(ext)]
		path = filepath.Join(dir, fmt.Sprintf("%s_pass%s", filename, ext))
	}

	// Check if the file already exists
	_, err := os.Stat(path)
	if err == nil {
		Print("File "+path+" already exists. Appending number...\n", Yellow)
		// File exists, find a new filename
		dir := filepath.Dir(path)
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		filename := base[:len(base)-len(ext)]

		i := 1
		for {
			newPath := filepath.Join(dir, fmt.Sprintf("%s_%d%s", filename, i, ext))
			_, err := os.Stat(newPath)
			if os.IsNotExist(err) {
				path = newPath
				break
			}
			i++
		}
	}

	// Create or open the file for appending
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		Print("Could not create/open file "+err.Error()+"\n", Red)
		return nil, ""
	}
	fmt.Println("Created " + path)
	return file, path
}

func convertTime(pwdLastSet string) string {
	if pwdLastSet == "" {
		return ""
	}
	// Convert pwdLastSet value to a time.Time object
	interval, err := strconv.ParseInt(pwdLastSet, 10, 64)
	if err != nil {
		PrintFatal(err.Error())
	}
	lastSetTime := time.Unix(0, (interval-116444736000000000)*100)

	// Format lastSetTime as a human-readable string
	return lastSetTime.Format("2006-01-02")
}
