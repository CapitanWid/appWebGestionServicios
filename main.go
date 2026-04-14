package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type VirtualMachine struct {
	Name string `json:"name"`
}

func main() {
	// Crear carpeta temporal para los zips subidos
	os.MkdirAll("./uploads", 0755)

	// Rutas del Servidor Web
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "templates/index.html")
	})

	http.HandleFunc("/api/vms", handleListVMs)
	http.HandleFunc("/deploy", handleDeploy)
	http.HandleFunc("/service-control", handleServiceControl)
	http.HandleFunc("/api/status", handleStatus)
	http.HandleFunc("/api/logs", handleLogs)

	fmt.Println(">>> Servidor VM Core corriendo en http://localhost:8080")
	fmt.Println(">>> Modo de ejecución: ROOT")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// --- GESTIÓN DE DESPLIEGUE ---

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Metodo no permitido", 405)
		return
	}

	err := r.ParseMultipartForm(50 << 20)
	if err != nil {
		http.Error(w, "Error parseando formulario", 400)
		return
	}

	destPath := r.FormValue("destination")
	serviceName := r.FormValue("serviceName")
	vmTarget := r.FormValue("vm_target")
	user := "root"

	file, header, err := r.FormFile("appZip")
	if err != nil {
		http.Error(w, "Archivo .zip no recibido", 400)
		return
	}
	defer file.Close()

	localZipPath := filepath.Join("./uploads", header.Filename)
	dst, err := os.Create(localZipPath)
	if err != nil {
		http.Error(w, "Error al crear archivo temporal", 500)
		return
	}
	defer dst.Close()
	io.Copy(dst, file)

	vmIP := getVMIP(vmTarget)
	if vmIP == "" {
		http.Error(w, "La VM está apagada o no tiene IP asignada", 500)
		return
	}

	// 1. Crear directorio remoto antes de enviar nada
	err = runSSH(user, vmIP, fmt.Sprintf("mkdir -p %s", destPath))
	if err != nil {
		http.Error(w, "Error creando carpeta remota: "+err.Error(), 500)
		return
	}

	// 2. Enviar el archivo vía SCP a la carpeta de root
	remoteTempPath := fmt.Sprintf("/root/%s", header.Filename)
	scpCmd := exec.Command("scp", "-o", "StrictHostKeyChecking=no", localZipPath, fmt.Sprintf("%s@%s:%s", user, vmIP, remoteTempPath))
	if out, err := scpCmd.CombinedOutput(); err != nil {
		http.Error(w, "Error SCP: "+string(out), 500)
		return
	}

	// 3. Instalación remota
	commands := []string{
		fmt.Sprintf("mv %s %s/ 2>/dev/null || true", remoteTempPath, destPath),
		fmt.Sprintf("cd %s && unzip -o %s", destPath, header.Filename),
		fmt.Sprintf("cd %s && mv *.sh %s.sh || true", destPath, serviceName),
		fmt.Sprintf("chmod +x %s/%s.sh", destPath, serviceName),
	}

	for _, cmd := range commands {
		if err := runSSH(user, vmIP, cmd); err != nil {
			http.Error(w, "Fallo en paso de instalación: "+err.Error(), 500)
			return
		}
	}

	// 4. Configuración del Servicio Systemd
	serviceBody := fmt.Sprintf(`[Unit]
Description=Servicio %s controlado por VM Core
After=network.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s
ExecStart=/bin/bash %s/%s.sh
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target`, serviceName, user, destPath, destPath, serviceName)

	createServiceCmd := fmt.Sprintf("echo '%s' | tee /etc/systemd/system/%s.service > /dev/null", serviceBody, serviceName)

	systemdCmds := []string{
		createServiceCmd,
		"systemctl daemon-reload",
		fmt.Sprintf("systemctl enable %s.service", serviceName),
		fmt.Sprintf("systemctl restart %s.service", serviceName),
	}

	for _, cmd := range systemdCmds {
		if err := runSSH(user, vmIP, cmd); err != nil {
			http.Error(w, "Error en Systemd: "+err.Error(), 500)
			return
		}
	}

	fmt.Fprintf(w, "Despliegue exitoso de %s en %s", serviceName, vmIP)
}

// --- CONTROL DE SERVICIOS Y STATUS ---

func handleServiceControl(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	service := r.URL.Query().Get("service")
	vmName := r.URL.Query().Get("vm")
	user := "root"

	vmIP := getVMIP(vmName)
	if vmIP == "" {
		http.Error(w, "VM no alcanzable", 500)
		return
	}

	cmd := fmt.Sprintf("systemctl %s %s.service", action, service)
	if err := runSSH(user, vmIP, cmd); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	fmt.Fprintf(w, "OK")
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	vmName := r.URL.Query().Get("vm")
	vmIP := getVMIP(vmName)
	user := "root"

	if vmIP == "" || service == "" {
		fmt.Fprint(w, "unknown")
		return
	}

	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", fmt.Sprintf("%s@%s", user, vmIP), fmt.Sprintf("systemctl is-active %s.service", service))
	out, _ := cmd.CombinedOutput()
	status := strings.TrimSpace(string(out))

	if status == "active" {
		fmt.Fprint(w, "active")
	} else {
		fmt.Fprint(w, "inactive")
	}
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	vmName := r.URL.Query().Get("vm")
	vmIP := getVMIP(vmName)
	user := "root"

	if vmIP == "" || service == "" {
		fmt.Fprint(w, "Consola: Esperando conexión...")
		return
	}

	logFile := fmt.Sprintf("/root/%s.log", service)
	cmd := fmt.Sprintf("tail -n 15 %s 2>/dev/null || echo 'No hay actividad en el log'", logFile)

	sshCmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", fmt.Sprintf("%s@%s", user, vmIP), cmd)
	out, _ := sshCmd.Output()

	if len(out) == 0 {
		fmt.Fprint(w, "Conectado. Esperando datos...")
	} else {
		fmt.Fprint(w, string(out))
	}
}

// --- INTEGRACIÓN CON VIRTUALBOX (EL MOTOR) ---

func handleListVMs(w http.ResponseWriter, r *http.Request) {
	vms := getVirtualBoxVMs()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vms)
}

func getVirtualBoxVMs() []VirtualMachine {
	var vms []VirtualMachine
	// Filtramos por tu grupo específico
	grupoObjetivo := "/Nuevo grupo 2"

	out, err := exec.Command("vboxmanage", "list", "vms").Output()
	if err != nil {
		return vms
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		parts := strings.Split(line, "\"")
		if len(parts) > 1 {
			name := parts[1]
			info, _ := exec.Command("vboxmanage", "showvminfo", name, "--machinereadable").Output()
			if strings.Contains(string(info), "groups=\""+grupoObjetivo+"\"") {
				vms = append(vms, VirtualMachine{Name: name})
			}
		}
	}
	return vms
}

func getVMIP(vmName string) string {
	// Intentamos obtener la IP de la red 0 (Net 0)
	out, err := exec.Command("vboxmanage", "guestproperty", "get",
		vmName, "/VirtualBox/GuestInfo/Net/0/V4/IP").Output()

	if err != nil {
		return ""
	}

	output := string(out)
	if strings.Contains(output, "No value set!") {
		return ""
	}

	ip := strings.TrimPrefix(output, "Value: ")
	return strings.TrimSpace(ip)
}

func runSSH(user, ip, command string) error {
	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", fmt.Sprintf("%s@%s", user, ip), command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("SSH Fail: %s -> %s", command, string(out))
		return fmt.Errorf(string(out))
	}
	return nil
}
