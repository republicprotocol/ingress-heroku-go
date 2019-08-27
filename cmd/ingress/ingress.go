package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/getsentry/raven-go"
	renExContract "github.com/republicprotocol/renex-ingress-go/contract"
	"github.com/republicprotocol/renex-ingress-go/httpadapter"
	"github.com/republicprotocol/renex-ingress-go/ingress"
	"github.com/republicprotocol/republic-go/contract"
	"github.com/republicprotocol/republic-go/crypto"
	"github.com/republicprotocol/republic-go/grpc"
	"github.com/republicprotocol/republic-go/identity"
	"github.com/republicprotocol/republic-go/leveldb"
	"github.com/republicprotocol/republic-go/logger"
	"github.com/republicprotocol/republic-go/registry"
	"github.com/republicprotocol/republic-go/swarm"
)

func init() {
	sentryDSN := os.Getenv("SENTRY_DSN")
	if sentryDSN == "" {
		log.Fatalln("cannot find SENTRY_DSN environment variable")
	}
	raven.SetDSN(sentryDSN)
}

func main() {
	logger.SetFilterLevel(logger.LevelDebugLow)
	alpha := os.Getenv("ALPHA")
	if alpha == "" {
		alpha = "5"
	}
	alphaNum, err := strconv.Atoi(alpha)
	if err != nil {
		log.Fatal("cannot parse alpha factor")
	}

	done := make(chan struct{})
	defer close(done)
	defer logger.Info("shutting down...")

	networkParam := os.Getenv("NETWORK")
	if networkParam == "" {
		log.Fatalf("cannot read network environment")
	}
	configParam := fmt.Sprintf("env/%v/config.json", networkParam)
	keystoreParam := fmt.Sprintf("env/%v/%v.keystore.json", networkParam, os.Getenv("DYNO"))
	keystorePassphraseParam := os.Getenv("KEYSTORE_PASSPHRASE")
	dbParam := os.Getenv("DATABASE_URL")
	kyberID := os.Getenv("KYBER_ID")
	kyberSecret := os.Getenv("KYBER_SECRET")

	config, err := loadConfig(configParam)
	if err != nil {
		log.Fatalf("cannot load config: %v", err)
	}
	infuraKey := os.Getenv("INFURA_KEY")
	if infuraKey == "" {
		panic("infuraKey cannot be empty")
	}
	if config.RepublicEthereum.URI == "" {
		var network string
		switch config.RepublicEthereum.Network {
		case contract.NetworkMainnet:
			network = "mainnet"
		default:
			network = "kovan"
		}
		config.RepublicEthereum.URI = fmt.Sprintf("https://%v.infura.io/v3/%v", network, infuraKey)
	}

	keystore, err := loadKeystore(keystoreParam, keystorePassphraseParam)
	if err != nil {
		log.Fatalf("cannot load keystore: %v", err)
	}

	multiAddr, err := getMultiaddress(keystore, os.Getenv("PORT"))
	if err != nil {
		log.Fatalf("cannot get multi-address: %v", err)
	}
	conn, err := contract.Connect(config.RepublicEthereum)
	if err != nil {
		log.Fatalf("cannot connect to ethereum: %v", err)
	}
	auth := bind.NewKeyedTransactor(keystore.EcdsaKey.PrivateKey)
	binder, err := contract.NewBinder(auth, conn)
	if err != nil {
		log.Fatalf("cannot create contract binder: %v", err)
	}

	contractConn, err := renExContract.Connect(config.RenExEthereum)
	if err != nil {
		log.Fatalf("cannot connect to ethereum: %v", err)
	}
	contractBinder, err := renExContract.NewBinder(auth, contractConn)
	if err != nil {
		log.Fatalf("cannot create contract binder: %v", err)
	}
	swapper, err := ingress.NewSwapper(dbParam, contractBinder)
	if err != nil {
		log.Fatalf("cannot connect to the database: %v", err)
	}
	loginer, err := ingress.NewLoginer(dbParam)
	if err != nil {
		log.Fatalf("cannot connect to the database: %v", err)
	}

	// New database for persistent storage
	store, err := leveldb.NewStore(path.Join(os.Getenv("HOME"), "data"), 72*time.Hour)
	if err != nil {
		log.Fatalf("cannot open leveldb: %v", err)
	}
	defer store.Release()
	multiAddr.Signature, err = keystore.EcdsaKey.Sign(multiAddr.Hash())
	if err != nil {
		log.Fatal("cannot sign own multiAddress")
	}
	if err := store.SwarmMultiAddressStore().InsertMultiAddress(multiAddr); err != nil {
		log.Fatal("cannot store own multiAddress")
	}

	crypter := registry.NewCrypter(keystore, &binder, 256, time.Minute)
	swarmClient := grpc.NewSwarmClient(store.SwarmMultiAddressStore(), multiAddr.Address())
	swarmer := swarm.NewSwarmer(swarmClient, store.SwarmMultiAddressStore(), alphaNum, &crypter)

	orderbookClient := grpc.NewOrderbookClient()
	ingresser := ingress.NewIngress(keystore.EcdsaKey, &binder, &contractBinder, swarmer, orderbookClient, 4*time.Second, swapper, loginer)
	ingressAdapter := httpadapter.NewIngressAdapter(ingresser)

	go func() {
		// Add bootstrap nodes in the store or load from the file.
		for _, multiAddr := range config.BootstrapMultiAddresses {
			_, err := store.SwarmMultiAddressStore().MultiAddress(multiAddr.Address())
			if err == nil {
				// Only add bootstrap multi-addresses that are not already in the store.
				continue
			}
			if err != swarm.ErrMultiAddressNotFound {
				logger.Network(logger.LevelError, fmt.Sprintf("cannot get bootstrap multi-address from store: %v", err))
				continue
			}

			if err := store.SwarmMultiAddressStore().InsertMultiAddress(multiAddr); err != nil {
				logger.Network(logger.LevelError, fmt.Sprintf("cannot store bootstrap multiaddress in store: %v", err))
			}
		}
		peers, err := swarmer.Peers()
		if err != nil {
			log.Printf("[error] (bootstrap) cannot get connected peers: %v", err)
		}
		log.Printf("[info] connected to %v peers", len(peers)-1)

		syncErrs := ingresser.Sync(done)
		go func() {
			for err := range syncErrs {
				logger.Error(fmt.Sprintf("error syncing: %v", err))
			}
		}()

		processErrs := ingresser.ProcessRequests(done)
		go func() {
			for err := range processErrs {
				logger.Error(fmt.Sprintf("error processing: %v", err))
			}
		}()
	}()

	log.Printf("address %v", multiAddr)
	log.Printf("ethereum %v", auth.From.Hex())
	log.Printf("listening at 0.0.0.0:%v...", os.Getenv("PORT"))
	if err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%v", os.Getenv("PORT")), httpadapter.NewIngressServer(ingressAdapter, config.ApprovedTraders, kyberID, kyberSecret)); err != nil {
		log.Fatalf("error listening and serving: %v", err)
	}
}

func getMultiaddress(keystore crypto.Keystore, port string) (identity.MultiAddress, error) {
	if len(port) == 0 {
		return identity.MultiAddress{}, fmt.Errorf("cannot use nil port")
	}
	// Get our IP address
	ipInfoOut, err := exec.Command("curl", "https://ipinfo.io/ip").Output()
	if err != nil {
		return identity.MultiAddress{}, err
	}
	ipAddress := strings.Trim(string(ipInfoOut), "\n ")
	ingressMultiaddress, err := identity.NewMultiAddressFromString(fmt.Sprintf("/ip4/%s/tcp/%s/republic/%s", ipAddress, port, keystore.Address()))
	if err != nil {
		return identity.MultiAddress{}, fmt.Errorf("cannot obtain trader multi address %v", err)
	}
	return ingressMultiaddress, nil
}

func loadConfig(configFile string) (renExContract.Config, error) {
	file, err := os.Open(configFile)
	if err != nil {
		return renExContract.Config{}, err
	}
	defer file.Close()
	c := renExContract.Config{}
	if err := json.NewDecoder(file).Decode(&c); err != nil {
		return renExContract.Config{}, err
	}
	return c, nil
}

func loadKeystore(keystoreFile, passphrase string) (crypto.Keystore, error) {
	file, err := os.Open(keystoreFile)
	if err != nil {
		return crypto.Keystore{}, err
	}
	defer file.Close()

	if passphrase == "" {
		keystore := crypto.Keystore{}
		if err := json.NewDecoder(file).Decode(&keystore); err != nil {
			return keystore, err
		}
		return keystore, nil
	}

	keystore := crypto.Keystore{}
	keystoreData, err := ioutil.ReadAll(file)
	if err != nil {
		return keystore, err
	}
	if err := keystore.DecryptFromJSON(keystoreData, passphrase); err != nil {
		return keystore, err
	}
	return keystore, nil
}
