package pki

import (
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// WriteKubeconfig writes a kubeconfig file at path granting access to
// server, trusting ca and authenticating with client. clusterName,
// userName and contextName name the corresponding kubeconfig entries;
// contextName is also set as the current context.
func WriteKubeconfig(path, server string, ca *CA, client *Cert, clusterName, userName, contextName string) error {
	cfg := clientcmdapi.NewConfig()

	cfg.Clusters[clusterName] = &clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: ca.CertPEM,
	}

	cfg.AuthInfos[userName] = &clientcmdapi.AuthInfo{
		ClientCertificateData: client.CertPEM,
		ClientKeyData:         client.KeyPEM,
	}

	cfg.Contexts[contextName] = &clientcmdapi.Context{
		Cluster:  clusterName,
		AuthInfo: userName,
	}

	cfg.CurrentContext = contextName

	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return fmt.Errorf("writing kubeconfig to %s: %w", path, err)
	}

	return nil
}
