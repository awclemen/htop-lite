# htop-lite

## Project Description

`htop-lite` is a small terminal-based system monitor written in Go. It is inspired by `htop`, but simplified for a course project. The program displays live information about the computer's CPU usage, memory usage, network activity, and running processes.

The program runs directly inside the terminal and updates once per second. It uses Go goroutines and channels so that each part of the program can run independently while still sharing updated system information safely.

---

## Authors

Andy Clements  
Cora Clements  

Course: CSc 372  
Assignment: Learn a New Programming Language, Part III  
Language: Go  

---

## Features

- Displays total CPU usage
- Displays per-core CPU usage
- Displays memory usage
- Displays network upload and download rates
- Displays a scrollable list of running processes
- Allows sorting processes by CPU, memory, PID, or name
- Allows filtering processes by name
- Allows the selected process to be killed
- Supports clean shutdown with `q` or `Ctrl+C`
- Writes debugging and shutdown information to `htop-lite.log`

---

## Download Dependencies

Before running the program for the first time, download and clean up the Go module dependencies:

``bash
go mod tidy

---

## How to Run

From the main project folder, run:

``bash
go run .

