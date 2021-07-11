package main

import (
	"bufio"
	"fmt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type GpsClient struct {
	Ip   string `yaml:"ip"`
	Name string `yaml:"name"`
}

type ConfigData struct {
	Host          string      `yaml:"host"`
	Port          string      `yaml:"port"`
	MQTT_port     string      `yaml:"mqtt_port"`
	MQTT_host     string      `yaml:"mqtt_host"`
	MQTT_user     string      `yaml:"mqtt_user"`
	MQTT_password string      `yaml:"mqtt_password"`
	GpsClients    []GpsClient `yaml:"clients"`
}

var configData ConfigData
var mqttClient mqtt.Client

func gpsTimeToUTC(in string) string {
	if len(in) != 9 {
		return ""
	}

	out := in[:2] + ":" + in[2:4] + ":" + in[4:6]
	return out
}

func gpsDateTime(gps_date string, gps_time string) time.Time {
	t, err := time.Parse(
		"06-01-02 15:04:05 MST",
		gps_date[4:6]+"-"+gps_date[2:4]+"-"+gps_date[:2]+" "+gpsTimeToUTC(gps_time)+" UTC")
	if err != nil {
		log.Fatal(err)
	}

	return t
}

func gpsDDMTo(system string, coordinate string, direction string) string {
	var dms string
	var dd_deg string
	var dd_rest string
	dd_neg := ""

	c := strings.Split(coordinate, ".")
	if len(c[0]) < 4 || len(c[1]) < 2 {
		fmt.Println("Invalid coordinates %s\n", coordinate)
	}

	if direction == "N" || direction == "S" {
		dms = direction + c[0][:2] + "°" + c[0][2:4] + "'"
		if direction == "S" {
			dd_neg = "-"
		}
		dd_deg = coordinate[:2]
		dd_rest = coordinate[2:]
	} else if direction == "E" || direction == "W" {
		dms = direction + c[0][:3] + "°" + c[0][3:5] + "'"
		if direction == "W" {
			dd_neg = "-"
		}
		dd_deg = coordinate[:3]
		dd_rest = coordinate[3:]
	} else {
		return "EDIR"
	}

	if system == "DMS" {
		f, _ := strconv.ParseFloat("0."+c[1], 64)
		f *= 60

		dms += fmt.Sprintf("%f\"", f)
		return dms
	} else if system == "DD" {
		rest_f, _ := strconv.ParseFloat(dd_rest, 64)
		rest_f /= 60

		deg_f, _ := strconv.ParseFloat(dd_deg, 64)

		dd_f := deg_f + rest_f
		dd := fmt.Sprintf("%s%f", dd_neg, dd_f)
		return dd
	} else {
		return "ESYSTEM"
	}
}

func gpsSpeedToKmh(knots string) string {
	knots_f, _ := strconv.ParseFloat(knots, 64)
	return fmt.Sprintf("%f", knots_f*1.852)
}

func publish(base_topic string, topic string, text string) {
	token := mqttClient.Publish(base_topic+topic, 0, true, text)
	token.Wait()
}

func handleData(c net.Conn) {
	var client_name string

	for _, client := range configData.GpsClients {
		if addr, ok := c.RemoteAddr().(*net.TCPAddr); ok {
			if addr.IP.String() == client.Ip {
				client_name = client.Name
			}
		}
	}

	if client_name == "" {
		fmt.Printf("Client name for %s is empty\n", c.RemoteAddr().String())
		c.Close()
		return
	}

	base_topic := "gps-proxy/" + client_name + "/"

	for {
		data, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			//fmt.Println(err)
			return
		}

		line := strings.TrimSpace(string(data))

		if len(line) < 6 {
			fmt.Printf("[%s] Unexpected short line: %s\n", client_name, line)
		} else {
			values := strings.Split(line, ",")
			len := len(values)

			// TODO: cut off checksum and calculate
			switch values[0] {
			case "$GPGGA":
				publish(base_topic, "GPGGA/utc", gpsTimeToUTC(values[1]))
				publish(base_topic, "GPGGA/lat_dms", gpsDDMTo("DMS", values[2], values[3]))
				publish(base_topic, "GPGGA/lon_dms", gpsDDMTo("DMS", values[4], values[5]))
				publish(base_topic, "GPGGA/lat_dd", gpsDDMTo("DD", values[2], values[3]))
				publish(base_topic, "GPGGA/lon_dd", gpsDDMTo("DD", values[4], values[5]))
				publish(base_topic, "GPGGA/quality", values[6])
				publish(base_topic, "GPGGA/sats", values[7])
				publish(base_topic, "GPGGA/hdop", values[8])
				publish(base_topic, "GPGGA/alt", values[9])
				publish(base_topic, "GPGGA/alt_unit", values[10])
				// undulation and differential data is ignored for now
			case "$GPRMC":
				// check status header
				// add track true, mag var, var dir, mode ind fields
				fmt.Printf("[%s] GPRMC: %s, %s %s speed %s\n", client_name,
					gpsTimeToUTC(values[1]),
					gpsDDMTo("DMS", values[3], values[4]),
					gpsDDMTo("DMS", values[5], values[6]),
					gpsSpeedToKmh(values[7]),
				)
				gps_time := gpsDateTime(values[9], values[1])
				publish(base_topic, "GPRMC/time", gpsTimeToUTC(values[1]))
				publish(base_topic, "GPRMC/lat_dms", gpsDDMTo("DMS", values[3], values[4]))
				publish(base_topic, "GPRMC/lon_dms", gpsDDMTo("DMS", values[5], values[6]))
				publish(base_topic, "GPRMC/lat_dd", gpsDDMTo("DD", values[3], values[4]))
				publish(base_topic, "GPRMC/lon_dd", gpsDDMTo("DD", values[5], values[6]))
				publish(base_topic, "GPRMC/speed_kmh", gpsSpeedToKmh(values[7]))
				publish(base_topic, "GPRMC/date_time", gps_time.Format("2006-01-02 15:04:05"))
				publish(base_topic, "GPRMC/date_time_local", gps_time.Local().Format("2006-01-02 15:04:05"))
			case "$GPGSA":
				publish(base_topic, "GPGSA/field_length", strconv.Itoa(len))
				publish(base_topic, "GPGSA/mode_ma", values[1])
				publish(base_topic, "GPGSA/mode_123", values[2])
				// TODO: print satellite info in fields 3-14
				publish(base_topic, "GPGSA/pdop", values[15])
				publish(base_topic, "GPGSA/hdop", values[16])
				publish(base_topic, "GPGSA/vdop", values[17])
			case "$GPGSV":
				publish(base_topic, "GPGSV/field_length", strconv.Itoa(len))
			case "$GPVTG":
				publish(base_topic, "GPVTG/track_true", values[1])
				publish(base_topic, "GPVTG/track_mag", values[3])
				publish(base_topic, "GPVTG/speed_kn", values[5])
				publish(base_topic, "GPVTG/speed_kmh", values[7])
				publish(base_topic, "GPVTG/mode_indicator", values[9])
			default:
				fmt.Printf("[%s] Unexpected line: %s\n", client_name, line)
			}
		}
	}
	c.Close()
}

func main() {
	config_dir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal("Unable to determine user config dir")
	}

	user_config_path := config_dir + "/gps-proxy.yml"
	sys_config_path := "/etc/gps-proxy.yml"
	active_config := user_config_path

	_, err = os.Stat(user_config_path)
	if os.IsNotExist(err) {
		active_config = sys_config_path
		_, err = os.Stat(sys_config_path)
		if os.IsNotExist(err) {
			log.Fatal("Configurations not found: " +
				user_config_path + ", " + sys_config_path)
		}
	}

	configFile, err := ioutil.ReadFile(active_config)
	if err != nil {
		log.Printf("Error reading configuration file #%v ", err)
	}

	err = yaml.Unmarshal(configFile, &configData)
	if err != nil {
		log.Fatalf("Error during Unmarshal: %v", err)
	}

	if configData.Host == "" {
		configData.Host = "0.0.0.0"
	}

	if configData.Port == "" {
		log.Fatal("Listen port not specified")
	}

	if configData.MQTT_port == "" {
		configData.MQTT_port = "1883"
	}

	l, err := net.Listen("tcp4", configData.Host+":"+configData.Port)
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()

	if configData.MQTT_host == "" {
		log.Fatal("No MQTT configuration found")
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%s",
		configData.MQTT_host,
		configData.MQTT_port))
	opts.SetClientID("gps-proxy")
	opts.SetUsername(configData.MQTT_user)
	opts.SetPassword(configData.MQTT_password)
	//opts.OnConnect = connectHandler
	//opts.OnConnectionLost = connectLostHandle
	mqttClient = mqtt.NewClient(opts)

	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go handleData(c)
	}
}
