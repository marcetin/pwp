// pwp - a.k.a Portable WP is a POC to spin up local portable WordPress installations
// easily without any server configurations.
//
// It replaces the database engine with SQLite using a drop-in plugin. It also uses the
// built-in PHP server to serve up the WordPress installation.
//
// Note: Because we are using the built-in PHP server a new router.php file gets created
//       to serve up routing for a WordPress single site install.

package main

import (
	"archive/zip"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const WordPressDownload = "https://wordpress.org/latest.zip"
const SQLitePlugin = "https://downloads.wordpress.org/plugin/sqlite-integration.zip"
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

var logger = log.New(os.Stdout, "pwp:", log.LstdFlags)

// siteSettings is contains the values of the command line flags or defaults.
type siteSettings struct {
	host *string
	port *string
	path *string
	php  *string
}

// main is our entrypoint.
func main() {

	settings := &siteSettings{
		host: flag.String("host", "localhost", "name of host"),
		port: flag.String("port", "auto", "port to use"),
		path: flag.String("path", "wordpress", "installation path"),
		php:  flag.String("php", "php", "php executable"),
	}

	flag.Parse()

	// Determine what port we should be serving up.
	port, err := autoPort(settings)
	if err != nil {
		logger.Fatalf("something went wrong: %s", err)
	}
	settings.port = &port

	// Attempt to setup WordPress if it hasn't already been setup.
	if err := setup(settings); err != nil {
		logger.Fatal(err)
	}

	// Run the PHP server.
	if err = runServer(settings); err != nil {
		logger.Fatal(err)
	}

}

// setup is responsible for bootsrapping our WordPress instance.
func setup(settings *siteSettings) error {

	if _, err := os.Stat(*settings.path); os.IsNotExist(err) {
		// Download WordPress.
		if err := downloadWordPress(WordPressDownload, *settings.path); err != nil {
			return errors.New(fmt.Sprintf("could not download WordPress: %s", err))
		}

		// Extract WordPress.
		if err := extractWordPress(*settings.path+".zip", *settings.path); err != nil {
			return errors.New(fmt.Sprintf("could not extract WordPress: %s", err))
		}

		pluginPath := *settings.path + "/wp-content/plugins"

		// Download SQLite plugin.
		if err := downloadSqlLitePlugin(SQLitePlugin, pluginPath+"/plugin.zip"); err != nil {
			return errors.New(fmt.Sprintf("could not download SQLite plugin: %s", err))
		}

		// Extract SQLite plugin.
		if err := extractSqlLitePlugin(pluginPath + "/plugin.zip"); err != nil {
			return errors.New(fmt.Sprintf("could not extract SQLite plugin: %s", err))
		}

		// Config WordPress.
		if err := createConfig(settings); err != nil {
			return errors.New(fmt.Sprintf("could not create WordPress config: %s", err))
		}

		// Router file.
		if err := createRouter(settings); err != nil {
			return errors.New(fmt.Sprintf("router file fail: %s", err))
		}
	}
	return nil
}

// runServer uses a very crude shell exec to run the PHP server.
func runServer(settings *siteSettings) error {

	err := updateWordPressSettings(settings)
	if err != nil {
		return err
	}

	serve := exec.Command("php", "-S", fmt.Sprintf("%s:%s", *settings.host, *settings.port), "-t", *settings.path, *settings.path+"/router.php")
	browse := exec.Command("open", fmt.Sprintf("http://%s:%s", *settings.host, *settings.port))
	fmt.Println("Starting built-in PHP server.")
	fmt.Printf("http://%s:%s\n", *settings.host, *settings.port)
	fmt.Println("Press Ctl-C to exit.")
	err = serve.Start()
	if err != nil {
		logger.Fatal(err)
	}
	// No need to catch errors in case this is not supported (OSX)
	browse.Start()
	err = serve.Wait()
	log.Printf("Command finished with error: %v", err)

	return nil
}

// spinner is a little hack to give a sense of progress.
func spinner(delay time.Duration) {
	for {
		for _, r := range `-\|/` {
			fmt.Printf("\r%c", r)
			time.Sleep(delay)
		}
	}
}

// Handle file move.
func moveFile(file *zip.File, dst string, rootPath string) error {
	path := filepath.Join(dst, strings.TrimPrefix(file.Name, rootPath))
	if file.FileInfo().IsDir() {
		os.MkdirAll(path, file.Mode())
		return nil
	}

	// Get the file...
	fileReader, err := file.Open()
	if err != nil {
		return err
	}
	defer fileReader.Close()

	// Get the destination...
	targetFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
	if err != nil {
		return err
	}

	// Copy!
	if _, err := io.Copy(targetFile, fileReader); err != nil {
		return err
	}
	defer targetFile.Close()
	return nil
}

// downloadWordPress downloads WordPress.
func downloadWordPress(src, dst string) error {

	// Create destination file.
	out, err := os.Create(dst + ".zip")
	if err != nil {
		return err
	}
	defer out.Close()

	// Download WordPress.
	logger.Println("Downloading WordPress core.")
	go spinner(45 * time.Millisecond)
	resp, err := http.Get(src)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Write the file.
	_, err = io.Copy(out, resp.Body)

	if err != nil {
		return err
	}
	fmt.Printf("\r")

	return nil
}

// extractWordPress extracts our downloaded version then disposes of the archive file.
func extractWordPress(src, dst string) error {

	// Extract WordPress.
	logger.Println("Extracting WordPress to destination.")
	go spinner(45 * time.Millisecond)

	// Open the zip.
	reader, err := zip.OpenReader(src)
	if err != nil {
		return err
	}

	// Create the destination path.
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	// Get the root path in the zip (should be `wordpress/`).
	rootPath := ""
	for _, file := range reader.File {
		path := file.Name
		if !file.FileInfo().IsDir() {
			continue
		}
		if len(path) < len(rootPath) || rootPath == "" {
			rootPath = path
		}
	}

	// Copy each file from the zip to its location.
	for _, file := range reader.File {
		err := moveFile(file, dst, rootPath)
		if err != nil {
			logger.Fatal(err)
		}
	}

	os.Remove(src)
	fmt.Printf("\r")

	return nil
}

// downloadSqlLitePlugin downloads the db drop-in to enable SQLite databases.
func downloadSqlLitePlugin(src, dst string) error {

	// Create destination file.
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	// Download WordPress.
	logger.Println("Downloading SQLite Integration plugin.")
	go spinner(45 * time.Millisecond)
	resp, err := http.Get(src)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Write the file.
	_, err = io.Copy(out, resp.Body)

	if err != nil {
		return err
	}
	fmt.Printf("\r")

	return nil
}

// extractSqlLitePlugin extracts the SQLite plugin and moves the drop-in to the required location.
func extractSqlLitePlugin(src string) error {
	// Extract WordPress.
	logger.Println("Extracting SQlite plugin")
	go spinner(45 * time.Millisecond)

	// Open the zip.
	reader, err := zip.OpenReader(src)
	if err != nil {
		return err
	}

	// Get the root path in the zip (should be `wordpress/`).
	rootPath := ""
	for _, file := range reader.File {
		path := file.Name
		if !file.FileInfo().IsDir() {
			continue
		}
		if len(path) < len(rootPath) || rootPath == "" {
			rootPath = path
		}
	}

	// Create the destination path.
	dst := strings.TrimSuffix(src, "/plugin.zip") + "/" + rootPath
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	// Copy each file from the zip to its location.
	for _, file := range reader.File {
		err := moveFile(file, dst, rootPath)
		if err != nil {
			logger.Fatal(err)
		}
	}

	os.Rename(dst+"/db.php", dst+"../../db.php")

	os.Remove(src)
	fmt.Printf("\r")

	return nil
}

// calculateSalt calculates salts for our wp-config.php file.
func calculateSalt() string {
	var src = rand.NewSource(time.Now().UnixNano())

	n := 64
	b := make([]byte, n)
	// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
	for i, cache, remain := n-1, src.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = src.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			b[i] = letterBytes[idx]
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return string(b)
}

// createConfig creates the wp-config.php file.
func createConfig(settings *siteSettings) error {

	configFile := *settings.path + "/wp-config.php"

	f, err := os.Create(configFile)
	if err != nil {
		return err
	}
	defer f.Close()

	f.WriteString("<?php\n\n")
	// Database config.
	f.WriteString("/** Database Settings **/\n")
	f.WriteString("/** NOTE: We are using an SQLite integration so these are ignored right now. **/\n")
	f.WriteString(fmt.Sprintf("define( 'DB_NAME',     '%s' );\n", ""))
	f.WriteString(fmt.Sprintf("define( 'DB_USER',     '%s' );\n", ""))
	f.WriteString(fmt.Sprintf("define( 'DB_PASSWORD', '%s' );\n", ""))
	f.WriteString(fmt.Sprintf("define( 'DB_HOST',     '%s' );\n", ""))
	f.WriteString(fmt.Sprintf("define( 'DB_CHARSET',  '%s' );\n", ""))
	f.WriteString(fmt.Sprintf("define( 'DB_COLLATE',  '%s' );\n\n", ""))
	// Auth salts and keys.
	f.WriteString("/** Authentication Unique Keys and Salts. **/\n")
	f.WriteString(fmt.Sprintf("define( 'AUTH_KEY',         '%s' );\n", calculateSalt()))
	f.WriteString(fmt.Sprintf("define( 'SECURE_AUTH_KEY',  '%s' );\n", calculateSalt()))
	f.WriteString(fmt.Sprintf("define( 'LOGGED_IN_KEY',    '%s' );\n", calculateSalt()))
	f.WriteString(fmt.Sprintf("define( 'NONCE_KEY',        '%s' );\n", calculateSalt()))
	f.WriteString(fmt.Sprintf("define( 'AUTH_SALT',        '%s' );\n", calculateSalt()))
	f.WriteString(fmt.Sprintf("define( 'SECURE_AUTH_SALT', '%s' );\n", calculateSalt()))
	f.WriteString(fmt.Sprintf("define( 'LOGGED_IN_SALT',   '%s' );\n", calculateSalt()))
	f.WriteString(fmt.Sprintf("define( 'NONCE_SALT',       '%s' );\n\n", calculateSalt()))
	// Table prefix.
	f.WriteString(fmt.Sprintf("$table_prefix = '%s';\n\n", "wp_"))

	// Remainder of file.
	f.WriteString(`/* That's all, stop editing! Happy blogging. */

/** Absolute path to the WordPress directory. */
if ( ! defined( 'ABSPATH' ) )
	define( 'ABSPATH', dirname( __FILE__ ) . '/' );

/** Sets up WordPress vars and included files. */
require_once ABSPATH . 'wp-settings.php';
`)

	f.Sync()

	return nil
}

// createRouted creates a basic router for the PHP stand alone server.
func createRouter(settings *siteSettings) error {

	configFile := *settings.path + "/router.php"

	f, err := os.Create(configFile)
	if err != nil {
		return err
	}
	defer f.Close()

	f.WriteString(`<?php
$root = $_SERVER['DOCUMENT_ROOT'];
chdir( $root );
$path = '/'.ltrim( parse_url( $_SERVER['REQUEST_URI'] )['path'],'/' );
if ( file_exists( $root.$path ) )
{
	if ( is_dir( $root.$path ) && substr( $path,strlen( $path ) - 1, 1 ) !== '/' )
	{
		header( 'Location: '.rtrim( $path,'/' ).'/' );
		exit;
	}
	if ( strpos( $path,'.php' ) === false )
	{
		return false;
	} else {
		chdir( dirname( $root.$path ) );
		require_once $root.$path;
	}
} else {
	include_once 'index.php';
}`)

	f.Sync()

	return nil
}

// autoPort determines the port the site should run on.
//
// It will first check if `-port` was provided and use the given port. If no
// value is provided it will attempt to use port 80, failing that it will
// let the kernel decide the port.
func autoPort(settings *siteSettings) (string, error) {

	// If a port has been provided, skip.
	if *settings.port != "auto" {
		return *settings.port, nil
	}

	// Try for port 80 first, else get one from kernel.
	addr, err := net.ResolveTCPAddr("tcp", *settings.host+":80")
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return getFreePort(*settings.host)
	}
	l.Close()

	// Port 80 it is.
	return "80", nil
}

// getFreePort asks the kernel for a free open port that is ready to use.
func getFreePort(host string) (string, error) {
	addr, err := net.ResolveTCPAddr("tcp", host+":0")
	if err != nil {
		return "", err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return "", err
	}
	defer l.Close()

	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// updateWordPressSettings updates our urls in our site to match the current host:port configuration
// only if it determines a change.
func updateWordPressSettings(settings *siteSettings) error {

	dbFile := fmt.Sprintf("%s/wp-content/database/.ht.sqlite", *settings.path)
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		return nil // Its likely that WP is not installed yet.
	}

	database, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		return err
	}
	defer database.Close()

	// Get old url.
	rows, err := database.Query("SELECT option_value FROM wp_options WHERE option_name = 'home';")
	if err != nil {
		return nil // Its likely that WP is not installed yet.
	}
	var oldUrl string
	for rows.Next() {
		rows.Scan(&oldUrl)
	}

	var newUrl string
	if "80" != *settings.port {
		newUrl = fmt.Sprintf("http://%s:%s", *settings.host, *settings.port)
	} else {
		newUrl = fmt.Sprintf("http://%s", *settings.host)
	}

	// If its the same url, do nothing.
	if oldUrl == newUrl {
		return nil
	}

	statement, err := database.Prepare("UPDATE wp_options SET option_value = ? WHERE option_name = 'home' OR option_name = 'siteurl';")
	if err != nil {
		return err
	}
	_, err = statement.Exec(newUrl)
	if err != nil {
		return err
	}

	return nil
	statement, _ = database.Prepare("UPDATE wp_posts SET guid = replace(guid, ?,?);")
	_, err = statement.Exec(oldUrl, newUrl)
	if err != nil {
		return err
	}

	statement, _ = database.Prepare("UPDATE wp_posts SET post_content = replace(post_content, ?, ?);")
	_, err = statement.Exec(oldUrl, newUrl)
	if err != nil {
		return err
	}

	statement, _ = database.Prepare("UPDATE wp_postmeta SET meta_value = replace(meta_value,?,?);")
	_, err = statement.Exec(oldUrl, newUrl)
	if err != nil {
		return err
	}

	return nil
}
