package imagebridge

import "github.com/gin-gonic/gin"

const contextKey = "native_image_bridge_intent"

func SetContext(c *gin.Context, intent Intent) {
	if c == nil {
		return
	}
	c.Set(contextKey, &intent)
}

func FromContext(c *gin.Context) (*Intent, bool) {
	if c == nil {
		return nil, false
	}
	value, exists := c.Get(contextKey)
	if !exists {
		return nil, false
	}
	intent, ok := value.(*Intent)
	return intent, ok && intent != nil
}
